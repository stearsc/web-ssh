package maintenance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	secret "github.com/stearsc/web-ssh/backend/internal/crypto"
	"github.com/stearsc/web-ssh/backend/internal/privatekey"
	"golang.org/x/sys/unix"
)

type Entry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Reference struct {
	ID     string `json:"object_id"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
}
type Manifest struct {
	Format                int         `json:"format"`
	Program               string      `json:"program_version"`
	Schema                []int64     `json:"schema_versions"`
	CreatedAt             string      `json:"created_at"`
	MasterKeyVerification string      `json:"master_key_verification"`
	KeyCustody            string      `json:"key_custody"`
	Files                 []Entry     `json:"files"`
	Objects               []Reference `json:"objects"`
}

func programVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		version := info.Main.Version
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				version += "/" + setting.Value
			}
			if setting.Key == "vcs.modified" && setting.Value == "true" {
				version += "/modified"
			}
		}
		if version != "" {
			return version
		}
	}
	return "development-6-K"
}

// openStaticDB 只读静止数据库，不迁移、不 VACUUM、不修改生产配置。
func openStaticDB(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

func schemaVersions(ctx context.Context, db *sql.DB) ([]int64, error) {
	// 只读取版本不足以证明静止副本可恢复；损坏页和孤立引用都必须拒绝。
	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		return nil, errors.New("恢复集数据库完整性校验失败")
	}
	violations, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return nil, errors.New("恢复集数据库引用校验失败")
	}
	hasViolation := violations.Next()
	if err = errors.Join(violations.Err(), violations.Close()); err != nil || hasViolation {
		return nil, errors.New("恢复集数据库引用校验失败")
	}
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, errors.New("恢复集数据结构不可读")
	}
	versions := []int64{}
	for rows.Next() {
		var version int64
		if err = rows.Scan(&version); err != nil {
			break
		}
		versions = append(versions, version)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil || !slices.Equal(versions, []int64{1, 2}) {
		return nil, errors.New("恢复集数据结构版本不受支持")
	}
	return versions, nil
}

func references(ctx context.Context, db *sql.DB) ([]Reference, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT o.id,o.byte_size,o.sha256 FROM private_key_objects o JOIN servers s ON s.private_key_object_id=o.id ORDER BY o.id`)
	if err != nil {
		return nil, errors.New("私钥引用不可读")
	}
	result := []Reference{}
	for rows.Next() {
		var r Reference
		var digest []byte
		if err = rows.Scan(&r.ID, &r.Size, &digest); err != nil {
			break
		}
		r.SHA256 = hex.EncodeToString(digest)
		result = append(result, r)
	}
	return result, errors.Join(err, rows.Err(), rows.Close())
}

func verifySecrets(ctx context.Context, db *sql.DB, key []byte, readKey func(string) ([]byte, error)) (bool, error) {
	cipher, err := secret.New(key)
	if err != nil {
		return false, errors.New("恢复集主密钥无效")
	}
	// 先关闭游标再读取文件，避免一连接池下嵌套查询死锁。
	type credential struct {
		id, mode, object     string
		password, passphrase []byte
	}
	rows, err := db.QueryContext(ctx, `SELECT id,auth_type,COALESCE(private_key_object_id,''),password_encrypted,passphrase_encrypted FROM servers`)
	if err != nil {
		return false, errors.New("恢复集凭据不可读")
	}
	credentials := []credential{}
	for rows.Next() {
		var c credential
		if err = rows.Scan(&c.id, &c.mode, &c.object, &c.password, &c.passphrase); err != nil {
			break
		}
		credentials = append(credentials, c)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return false, errors.New("恢复集凭据不可读")
	}
	hasMaterial := false
	for _, c := range credentials {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if c.mode == "password" {
			plain, err := cipher.Decrypt(c.password)
			clear(plain)
			if err != nil {
				return false, errors.New("保存密码无法解密，可能是主密钥或数据问题")
			}
			hasMaterial = true
		} else if c.mode == "private_key" {
			var passphrase []byte
			if len(c.passphrase) > 0 {
				passphrase, err = cipher.DecryptPassphrase(c.id, c.passphrase)
				if err != nil {
					return false, err
				}
				hasMaterial = true
			}
			raw, readErr := readKey(c.object)
			if readErr == nil {
				_, readErr = privatekey.Parse(ctx, raw, passphrase)
			}
			clear(raw)
			clear(passphrase)
			if readErr != nil {
				return false, errors.New("恢复集引用私钥无效或不完整")
			}
		} else {
			return false, errors.New("恢复集认证类型无效")
		}
	}
	return hasMaterial, nil
}

func createPrivate(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return errors.New("恢复集文件创建失败")
	}
	n, writeErr := file.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	return errors.Join(writeErr, file.Sync(), file.Close())
}

func copyPrivate(source, destination string) error {
	file, err := os.OpenFile(source, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("静止备份源文件不可读")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("静止备份源文件类型无效")
	}
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return errors.New("恢复集文件创建失败")
	}
	_, err = io.Copy(out, file)
	return errors.Join(err, out.Sync(), out.Close())
}

func entry(root *os.Root, name string) (Entry, error) {
	result := Entry{Path: name}
	file, err := root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return result, errors.New("恢复集文件不可读")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return result, errors.New("恢复集文件权限或类型无效")
	}
	hash := sha256.New()
	result.Size, err = io.Copy(hash, file)
	result.SHA256 = hex.EncodeToString(hash.Sum(nil))
	return result, err
}

// Backup 必须在持有服务互斥锁且服务已经停止后调用；新目标失败时不标记完整。
func Backup(ctx context.Context, databasePath, keyDirectory, destination string, key []byte) (Manifest, error) {
	manifest := Manifest{Format: 1, Program: programVersion(), CreatedAt: time.Now().UTC().Format(time.RFC3339), KeyCustody: "secrets/app-master-key；须另存独立受限副本；遗失后不得生成新密钥替代"}
	if len(key) != 32 {
		return manifest, errors.New("主密钥格式无效")
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return manifest, errors.New("备份目标必须不存在且父目录可写")
	}
	for _, name := range []string{"database", "ssh-keys", "secrets"} {
		if err := os.Mkdir(filepath.Join(destination, name), 0700); err != nil {
			return manifest, errors.New("备份目录创建失败")
		}
	}
	if err := copyPrivate(databasePath, filepath.Join(destination, "database/web-ssh.db")); err != nil {
		return manifest, err
	}
	names := []string{"database/web-ssh.db"}
	if _, err := os.Lstat(databasePath + "-wal"); err == nil {
		if err = copyPrivate(databasePath+"-wal", filepath.Join(destination, "database/web-ssh.db-wal")); err != nil {
			return manifest, err
		}
		names = append(names, "database/web-ssh.db-wal")
	} else if !errors.Is(err, os.ErrNotExist) {
		return manifest, errors.New("SQLite WAL 不可读")
	}
	db, err := openStaticDB(filepath.Join(destination, "database/web-ssh.db"))
	if err != nil {
		return manifest, err
	}
	manifest.Schema, err = schemaVersions(ctx, db)
	if err != nil {
		db.Close()
		return manifest, err
	}
	manifest.Objects, err = references(ctx, db)
	db.Close()
	if err != nil {
		return manifest, err
	}
	source, err := openStaticDB(databasePath)
	if err != nil {
		return manifest, err
	}
	defer source.Close()
	store := privatekey.NewStore(source, keyDirectory, 24*time.Hour)
	for _, object := range manifest.Objects {
		lease, err := store.Acquire(ctx, object.ID)
		if err != nil {
			return manifest, errors.New("备份源私钥引用不可用")
		}
		raw, err := store.Read(ctx, object.ID)
		lease.Release()
		if err != nil {
			return manifest, errors.New("备份源私钥文件不完整")
		}
		err = createPrivate(filepath.Join(destination, "ssh-keys", object.ID+".key"), raw)
		clear(raw)
		if err != nil {
			return manifest, err
		}
		names = append(names, "ssh-keys/"+object.ID+".key")
	}
	if err = createPrivate(filepath.Join(destination, "secrets/app-master-key"), []byte(base64.StdEncoding.EncodeToString(key)+"\n")); err != nil {
		return manifest, err
	}
	if err = createPrivate(filepath.Join(destination, "secrets/custody.txt"), []byte(manifest.KeyCustody+"\n本恢复集包含敏感原始私钥；独立副本与访问权限由运维管理。\n")); err != nil {
		return manifest, err
	}
	names = append(names, "secrets/app-master-key", "secrets/custody.txt")
	root, err := os.OpenRoot(destination)
	if err != nil {
		return manifest, errors.New("备份目标不可读")
	}
	defer root.Close()
	checkDB, err := openStaticDB(filepath.Join(destination, "database/web-ssh.db"))
	if err != nil {
		return manifest, err
	}
	hasMaterial, err := verifySecrets(ctx, checkDB, key, func(id string) ([]byte, error) { return root.ReadFile("ssh-keys/" + id + ".key") })
	closeErr := checkDB.Close()
	if err != nil || closeErr != nil {
		return manifest, errors.Join(err, closeErr)
	}
	manifest.MasterKeyVerification = "无法验证历史匹配性"
	if hasMaterial {
		manifest.MasterKeyVerification = "历史密文解密验证成功"
	}
	for _, name := range names {
		e, err := entry(root, name)
		if err != nil {
			return manifest, err
		}
		manifest.Files = append(manifest.Files, e)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return manifest, err
	}
	if err = createPrivate(filepath.Join(destination, "manifest.json"), append(data, '\n')); err != nil {
		return manifest, err
	}
	if err = syncDirectories(destination); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func syncDirectories(path string) error {
	for _, suffix := range []string{"database", "ssh-keys", "secrets", "."} {
		file, err := os.Open(filepath.Join(path, suffix))
		if err != nil {
			return errors.New("恢复集目录同步失败")
		}
		err = errors.Join(file.Sync(), file.Close())
		if err != nil {
			return errors.New("恢复集目录同步失败")
		}
	}
	return nil
}

func safeObjectID(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}

// Verify 使用归档自身密钥和文件，永不访问当前 production 私钥目录。
func Verify(ctx context.Context, path string) (Manifest, error) {
	manifest := Manifest{}
	root, err := os.OpenRoot(path)
	if err != nil {
		return manifest, errors.New("恢复集目录不可读")
	}
	defer root.Close()
	for _, name := range []string{".", "database", "ssh-keys", "secrets"} {
		info, err := root.Lstat(name)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			return manifest, errors.New("恢复集目录权限或类型无效")
		}
	}
	file, err := root.OpenFile("manifest.json", os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return manifest, errors.New("恢复集清单缺失")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 16*1024*1024))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&manifest)
	if err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			err = errors.New("清单含多余内容")
		}
	}
	file.Close()
	if err != nil || manifest.Format != 1 || manifest.Program == "" || !slices.Equal(manifest.Schema, []int64{1, 2}) || manifest.KeyCustody == "" {
		return manifest, errors.New("恢复集清单格式或版本无效")
	}
	expected := map[string]bool{"database/web-ssh.db": true, "secrets/app-master-key": true, "secrets/custody.txt": true}
	for _, object := range manifest.Objects {
		if !safeObjectID(object.ID) || object.Size < 1 || object.Size > privatekey.MaxFileSize || len(object.SHA256) != 64 {
			return manifest, errors.New("恢复集对象清单无效")
		}
		name := "ssh-keys/" + object.ID + ".key"
		if expected[name] {
			return manifest, errors.New("恢复集对象重复")
		}
		expected[name] = true
	}
	seen := map[string]Entry{}
	for _, want := range manifest.Files {
		if (!expected[want.Path] && want.Path != "database/web-ssh.db-wal") || seen[want.Path].Path != "" {
			return manifest, errors.New("恢复集含非法或重复路径")
		}
		got, err := entry(root, want.Path)
		if err != nil || got != want {
			return manifest, errors.New("恢复集文件完整性校验失败")
		}
		seen[want.Path] = got
	}
	for name := range expected {
		if seen[name].Path == "" {
			return manifest, errors.New("恢复集文件不完整")
		}
	}
	// WAL 会参与 SQLite 读取，不能允许未纳入清单的有效内容覆盖已校验的 DB。
	// 只读打开 WAL 模式数据库可能建立空 WAL/SHM；空文件没有可恢复事务。
	if seen["database/web-ssh.db-wal"].Path == "" {
		info, err := root.Lstat("database/web-ssh.db-wal")
		if err == nil && (!info.Mode().IsRegular() || info.Size() != 0) || err != nil && !errors.Is(err, os.ErrNotExist) {
			return manifest, errors.New("恢复集含未登记的 SQLite WAL")
		}
	}
	keyData, err := root.ReadFile("secrets/app-master-key")
	if err != nil {
		return manifest, errors.New("恢复集主密钥缺失")
	}
	defer clear(keyData)
	key, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSuffix(string(keyData), "\n"))
	if err != nil || len(key) != 32 {
		return manifest, errors.New("恢复集主密钥格式错误")
	}
	defer clear(key)
	db, err := openStaticDB(filepath.Join(path, "database/web-ssh.db"))
	if err != nil {
		return manifest, err
	}
	defer db.Close()
	versions, err := schemaVersions(ctx, db)
	if err != nil || !slices.Equal(versions, manifest.Schema) {
		return manifest, errors.New("恢复集结构版本不一致")
	}
	refs, err := references(ctx, db)
	if err != nil || !slices.Equal(refs, manifest.Objects) {
		return manifest, errors.New("恢复集数据库与私钥引用不一致")
	}
	for _, object := range refs {
		file := seen["ssh-keys/"+object.ID+".key"]
		if file.Size != int64(object.Size) || file.SHA256 != object.SHA256 {
			return manifest, errors.New("恢复集私钥摘要不一致")
		}
	}
	hasMaterial, err := verifySecrets(ctx, db, key, func(id string) ([]byte, error) { return root.ReadFile("ssh-keys/" + id + ".key") })
	if err != nil {
		return manifest, err
	}
	state := "无法验证历史匹配性"
	if hasMaterial {
		state = "历史密文解密验证成功"
	}
	if manifest.MasterKeyVerification != state {
		return manifest, errors.New("恢复集主密钥验证状态不一致")
	}
	return manifest, nil
}

// Restore 仅恢复到没有数据库和私钥文件的新目标；不覆盖生产数据或替换主密钥来源。
func Restore(ctx context.Context, set, databasePath, keyDirectory string) error {
	manifest, err := Verify(ctx, set)
	if err != nil {
		return err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Lstat(databasePath + suffix); !errors.Is(err, os.ErrNotExist) {
			return errors.New("恢复数据库目标必须不存在")
		}
	}
	if info, err := os.Lstat(keyDirectory); err == nil {
		if !info.IsDir() || info.Mode().Perm() != 0700 {
			return errors.New("恢复私钥目录权限或类型无效")
		}
		names, err := os.ReadDir(keyDirectory)
		if err != nil || len(names) != 0 {
			return errors.New("恢复私钥目标必须为空")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("恢复私钥目标不可用")
	} else if err = os.MkdirAll(keyDirectory, 0700); err != nil {
		return errors.New("恢复私钥目录创建失败")
	}
	if err = os.MkdirAll(filepath.Dir(databasePath), 0700); err != nil {
		return errors.New("恢复数据库目录创建失败")
	}
	for _, e := range manifest.Files {
		destination := ""
		if strings.HasPrefix(e.Path, "database/") {
			destination = databasePath + strings.TrimPrefix(e.Path, "database/web-ssh.db")
		}
		if strings.HasPrefix(e.Path, "ssh-keys/") {
			destination = filepath.Join(keyDirectory, strings.TrimPrefix(e.Path, "ssh-keys/"))
		}
		if destination != "" {
			if err = copyPrivate(filepath.Join(set, e.Path), destination); err != nil {
				return err
			}
		}
	}
	// inode/device 属于物理目标，复制后重新登记；原始私钥字节和数据库凭据不变。
	absolute, _ := filepath.Abs(databasePath)
	uri := url.URL{Scheme: "file", Path: absolute}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return errors.New("恢复数据库不可读")
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("恢复对象归属登记失败")
	}
	defer tx.Rollback()
	for _, object := range manifest.Objects {
		var stat unix.Stat_t
		if unix.Lstat(filepath.Join(keyDirectory, object.ID+".key"), &stat) != nil {
			return errors.New("恢复对象归属登记失败")
		}
		if _, err = tx.ExecContext(ctx, `UPDATE private_key_objects SET file_device=?,file_inode=? WHERE id=?`, int64(stat.Dev), int64(stat.Ino), object.ID); err != nil {
			return errors.New("恢复对象归属登记失败")
		}
	}
	if err = tx.Commit(); err != nil {
		return errors.New("恢复对象归属登记未完成，不得启动服务")
	}
	// 成功必须以目标副本的实际字节为准，不能只依赖复制前的归档校验。
	if _, err = schemaVersions(ctx, db); err != nil {
		return err
	}
	archive, err := os.OpenRoot(set)
	if err != nil {
		return errors.New("恢复集主密钥不可读")
	}
	keyData, err := archive.ReadFile("secrets/app-master-key")
	archive.Close()
	if err != nil {
		return errors.New("恢复集主密钥不可读")
	}
	defer clear(keyData)
	key, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSuffix(string(keyData), "\n"))
	if err != nil || len(key) != 32 {
		return errors.New("恢复集主密钥格式错误")
	}
	defer clear(key)
	store := privatekey.NewStore(db, keyDirectory, 24*time.Hour)
	if _, err = verifySecrets(ctx, db, key, func(id string) ([]byte, error) {
		lease, err := store.Acquire(ctx, id)
		if err != nil {
			return nil, err
		}
		defer lease.Release()
		return store.Read(ctx, id)
	}); err != nil {
		return err
	}
	if err = db.Close(); err != nil {
		return errors.New("恢复数据库关闭失败")
	}
	for _, path := range []string{keyDirectory, filepath.Dir(databasePath)} {
		directory, err := os.Open(path)
		if err != nil {
			return errors.New("恢复目标目录同步失败")
		}
		if err = errors.Join(directory.Sync(), directory.Close()); err != nil {
			return errors.New("恢复目标目录同步失败")
		}
	}
	return nil
}
