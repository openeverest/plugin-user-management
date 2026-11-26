package seaweed

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

const (
	RoleRead      = "read"
	RoleReadWrite = "readWrite"
	RoleAdmin     = "admin"
)

var roleCatalog = []engine.Role{
	{
		Name:        RoleRead,
		Scope:       engine.ScopeBucket,
		Description: "List the bucket and read its objects.",
	},
	{
		Name:        RoleReadWrite,
		Scope:       engine.ScopeBucket,
		Description: "List, read, upload and delete objects in the bucket.",
		Includes:    []string{RoleRead},
	},
	{
		Name:        RoleAdmin,
		Scope:       engine.ScopeBucket,
		Description: "Perform any S3 operation on the bucket and its objects.",
		Includes:    []string{RoleRead, RoleReadWrite},
	},
}

var bucketNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

func normalizeRoles(grants []engine.RoleGrant) ([]engine.RoleGrant, error) {
	if len(grants) == 0 {
		return nil, fmt.Errorf("%w: at least one role is required", engine.ErrInvalid)
	}
	normalized := make([]engine.RoleGrant, 0, len(grants))
	seen := map[engine.RoleGrant]bool{}
	for _, grant := range grants {
		role, ok := roleByName(grant.Role)
		if !ok {
			return nil, fmt.Errorf("%w: role %q can't be granted", engine.ErrInvalid, grant.Role)
		}
		if role.Scope != engine.ScopeBucket || grant.Database != "" || !validBucketName(grant.Bucket) {
			return nil, fmt.Errorf("%w: role %q needs a valid S3 bucket name", engine.ErrInvalid, grant.Role)
		}
		next := engine.RoleGrant{Role: grant.Role, Bucket: grant.Bucket}
		if !seen[next] {
			seen[next] = true
			normalized = append(normalized, next)
		}
	}

	out := normalized[:0]
	for _, grant := range normalized {
		redundant := false
		for _, other := range normalized {
			otherRole, _ := roleByName(other.Role)
			if other.Bucket == grant.Bucket && slices.Contains(otherRole.Includes, grant.Role) {
				redundant = true
				break
			}
		}
		if !redundant {
			out = append(out, grant)
		}
	}
	return out, nil
}

func roleByName(name string) (engine.Role, bool) {
	for _, role := range roleCatalog {
		if role.Name == name {
			return role, true
		}
	}
	return engine.Role{}, false
}

func validBucketName(name string) bool {
	return bucketNamePattern.MatchString(name) &&
		!strings.Contains(name, "..") &&
		net.ParseIP(name) == nil
}

func policyStatements(grant engine.RoleGrant) []seaweedv1.S3PolicyStatement {
	bucket := grant.Bucket
	objects := bucket + "/*"
	switch grant.Role {
	case RoleRead:
		return []seaweedv1.S3PolicyStatement{
			statement([]string{"s3:ListBucket"}, []string{bucket}),
			statement([]string{"s3:GetObject", "s3:GetObjectVersion"}, []string{objects}),
		}
	case RoleReadWrite:
		return []seaweedv1.S3PolicyStatement{
			statement([]string{"s3:ListBucket"}, []string{bucket}),
			statement([]string{
				"s3:GetObject",
				"s3:GetObjectVersion",
				"s3:PutObject",
				"s3:DeleteObject",
				"s3:DeleteObjectVersion",
				"s3:AbortMultipartUpload",
			}, []string{objects}),
		}
	default:
		return []seaweedv1.S3PolicyStatement{
			statement([]string{"s3:*"}, []string{bucket, objects}),
		}
	}
}

func statement(actions, resources []string) seaweedv1.S3PolicyStatement {
	return seaweedv1.S3PolicyStatement{
		Effect:    seaweedv1.S3PolicyEffectAllow,
		Actions:   actions,
		Resources: resources,
	}
}
