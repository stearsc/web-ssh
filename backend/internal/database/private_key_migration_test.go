package database

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stearsc/web-ssh/backend/migrations"
)

func TestPrivateKeyMigrationPreservesOldDataCipherAndSessionCascade(t *testing.T) {
	db := openTest(t, filepath.Join(t.TempDir(), "old.db"), 100)
	initial, err := migrations.Files.ReadFile("001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err = migrate(context.Background(), db, fstest.MapFS{"001_initial.sql": &fstest.MapFile{Data: initial}}); err != nil {
		t.Fatal(err)
	}
	password := []byte{1, 4, 9, 0, 255}
	execTest(t, db, `INSERT INTO servers(id,name,host,port,username,password_encrypted,created_at,updated_at,trusted_host,trusted_port,host_key,host_key_algorithm,host_key_fingerprint) VALUES('old','old','localhost',22,'tester',?,10,20,'localhost',22,x'0102','algo','fingerprint')`, password)
	execTest(t, db, sessionInsert, "old")
	if err = Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	var mode string
	var after []byte
	var created, updated int64
	if err = db.QueryRow(`SELECT auth_type,password_encrypted,created_at,updated_at FROM servers WHERE id='old'`).Scan(&mode, &after, &created, &updated); err != nil || mode != "password" || !bytes.Equal(after, password) || created != 10 || updated != 20 {
		t.Fatal(mode, after, created, updated, err)
	}
	var count int
	db.QueryRow(`SELECT count(*) FROM sessions WHERE id='session' AND server_id='old' AND status='CONNECTED'`).Scan(&count)
	if count != 1 {
		t.Fatal("迁移丢失 Session")
	}
	var host, key, algorithm, fingerprint string
	var port int
	if err = db.QueryRow(`SELECT trusted_host,trusted_port,hex(host_key),host_key_algorithm,host_key_fingerprint FROM servers WHERE id='old'`).Scan(&host, &port, &key, &algorithm, &fingerprint); err != nil || host != "localhost" || port != 22 || key != "0102" || algorithm != "algo" || fingerprint != "fingerprint" {
		t.Fatal("信任信息变化", err)
	}
	execTest(t, db, `DELETE FROM servers WHERE id='old'`)
	db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&count)
	if count != 0 {
		t.Fatal("ON DELETE CASCADE 丢失")
	}
	for _, query := range []string{
		`INSERT INTO servers(id,name,host,port,username,auth_type,created_at,updated_at) VALUES('bad','x','localhost',22,'x','password',1,1)`,
		`INSERT INTO servers(id,name,host,port,username,auth_type,password_encrypted,private_key_object_id,created_at,updated_at) VALUES('bad','x','localhost',22,'x','private_key',x'01','absent',1,1)`,
	} {
		if _, err = db.Exec(query); err == nil {
			t.Fatal("认证互斥约束失效")
		}
	}
}
