package server

import (
	"context"
	"database/sql"
)

const columns = `id,name,host,port,username,auth_type,password_encrypted,private_key_object_id,passphrase_encrypted,group_name,remark,host_key,host_key_algorithm,host_key_fingerprint,created_at,updated_at`

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Record, error) {
	r := Record{}
	var algorithm, fingerprint, keyID sql.NullString
	err := row.Scan(&r.ID, &r.Name, &r.Host, &r.Port, &r.Username, &r.AuthType, &r.PasswordEncrypted, &keyID, &r.PassphraseEncrypted, &r.GroupName, &r.Remark, &r.PublicKey, &algorithm, &fingerprint, &r.CreatedAt, &r.UpdatedAt)
	r.PrivateKeyObjectID = keyID.String
	r.Algorithm = algorithm.String
	r.Fingerprint = fingerprint.String
	return r, err
}
func save(ctx context.Context, tx *sql.Tx, r Record, create bool) error {
	host, algorithm, fingerprint := sql.NullString{}, sql.NullString{}, sql.NullString{}
	port := sql.NullInt64{}
	if len(r.PublicKey) > 0 {
		host = sql.NullString{String: r.Host, Valid: true}
		port = sql.NullInt64{Int64: int64(r.Port), Valid: true}
		algorithm = sql.NullString{String: r.Algorithm, Valid: true}
		fingerprint = sql.NullString{String: r.Fingerprint, Valid: true}
	}
	// 信任五字段与目标资料同事务更新，目标变更不会留下旧目标的可信公钥。
	if create {
		_, err := tx.ExecContext(ctx, `INSERT INTO servers(id,name,host,port,username,auth_type,password_encrypted,private_key_object_id,passphrase_encrypted,group_name,remark,host_key,host_key_algorithm,host_key_fingerprint,created_at,updated_at,trusted_host,trusted_port) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, r.ID, r.Name, r.Host, r.Port, r.Username, r.AuthType, r.PasswordEncrypted, nullableID(r.PrivateKeyObjectID), r.PassphraseEncrypted, r.GroupName, r.Remark, r.PublicKey, algorithm, fingerprint, r.CreatedAt, r.UpdatedAt, host, port)
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE servers SET name=?,host=?,port=?,username=?,auth_type=?,password_encrypted=?,private_key_object_id=?,passphrase_encrypted=?,group_name=?,remark=?,host_key=?,host_key_algorithm=?,host_key_fingerprint=?,updated_at=?,trusted_host=?,trusted_port=? WHERE id=?`, r.Name, r.Host, r.Port, r.Username, r.AuthType, r.PasswordEncrypted, nullableID(r.PrivateKeyObjectID), r.PassphraseEncrypted, r.GroupName, r.Remark, r.PublicKey, algorithm, fingerprint, r.UpdatedAt, host, port, r.ID)
	return err
}

func nullableID(id string) any {
	if id == "" {
		return nil
	}
	return id
}
