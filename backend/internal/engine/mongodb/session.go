package mongodb

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

const (
	connectTimeout = 10 * time.Second
	// MongoDB error codes.
	codeUserNotFound = 11
	codeDuplicateKey = 51003
)

// dbUser is a user as returned by usersInfo.
type dbUser struct {
	ID    string      `bson:"_id"`
	User  string      `bson:"user"`
	DB    string      `bson:"db"`
	Roles []mongoRole `bson:"roles"`
}

// session runs user administration commands on a cluster.
type session interface {
	usersInfo(ctx context.Context) ([]dbUser, error)
	createUser(ctx context.Context, db, user, password string, roles []mongoRole) error
	// updateUser leaves the password unchanged when empty and the roles when nil.
	updateUser(ctx context.Context, db, user, password string, roles []mongoRole) error
	dropUser(ctx context.Context, db, user string) error
	close(ctx context.Context)
}

type dialer func(ctx context.Context, c cluster) (session, error)

type driverSession struct {
	client *mongo.Client
}

func dialDriver(_ context.Context, c cluster) (session, error) {
	opts := options.Client().
		SetHosts([]string{c.host}).
		SetAuth(options.Credential{Username: c.adminUser, Password: c.adminPass, AuthSource: adminDatabase}).
		SetConnectTimeout(connectTimeout).
		SetServerSelectionTimeout(connectTimeout).
		SetAppName("openeverest-plugin-user-management")
	if c.requireTLS {
		tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
		if len(c.caPEM) > 0 {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(c.caPEM) {
				return nil, errors.New("invalid MongoDB CA certificate")
			}
			tlsConfig.RootCAs = pool
		}
		opts.SetTLSConfig(tlsConfig)
	}
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, fmt.Errorf("connect to MongoDB: %w", err)
	}
	return &driverSession{client: client}, nil
}

func (s *driverSession) run(ctx context.Context, db string, cmd bson.D) error {
	err := s.client.Database(db).RunCommand(ctx, cmd).Err()
	var cmdErr mongo.CommandError
	if errors.As(err, &cmdErr) {
		switch cmdErr.Code {
		case codeUserNotFound:
			return engine.ErrNotFound
		case codeDuplicateKey:
			return engine.ErrConflict
		}
	}
	return err
}

func (s *driverSession) usersInfo(ctx context.Context) ([]dbUser, error) {
	var out struct {
		Users []dbUser `bson:"users"`
	}
	cmd := bson.D{{Key: "usersInfo", Value: bson.D{{Key: "forAllDBs", Value: true}}}}
	if err := s.client.Database(adminDatabase).RunCommand(ctx, cmd).Decode(&out); err != nil {
		return nil, fmt.Errorf("usersInfo: %w", err)
	}
	return out.Users, nil
}

func (s *driverSession) createUser(ctx context.Context, db, user, password string, roles []mongoRole) error {
	return s.run(ctx, db, bson.D{
		{Key: "createUser", Value: user},
		{Key: "pwd", Value: password},
		{Key: "roles", Value: roles},
	})
}

func (s *driverSession) updateUser(ctx context.Context, db, user, password string, roles []mongoRole) error {
	cmd := bson.D{{Key: "updateUser", Value: user}}
	if password != "" {
		cmd = append(cmd, bson.E{Key: "pwd", Value: password})
	}
	if roles != nil {
		cmd = append(cmd, bson.E{Key: "roles", Value: roles})
	}
	return s.run(ctx, db, cmd)
}

func (s *driverSession) dropUser(ctx context.Context, db, user string) error {
	return s.run(ctx, db, bson.D{{Key: "dropUser", Value: user}})
}

func (s *driverSession) close(ctx context.Context) {
	_ = s.client.Disconnect(ctx)
}
