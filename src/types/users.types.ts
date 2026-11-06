export type RoleScope = 'database' | 'cluster';

export interface Role {
  name: string;
  description: string;
  scope: RoleScope;
}

export interface RoleGrant {
  role: string;
  database?: string;
}

export type UserPhase = 'Pending' | 'Ready' | 'Failed' | 'Deleting';

export interface DbUser {
  // InstanceUser name; absent for users the plugin doesn't manage.
  id?: string;
  username: string;
  database?: string;
  roles: RoleGrant[];
  managed: boolean;
  system: boolean;
  phase?: UserPhase;
  message?: string;
  // The password lives in a Secret managed outside the plugin.
  ownPasswordSecret?: boolean;
}

// How users authenticate: database passwords, or API keys for models.
export type CredentialKind = 'password' | 'apiKey';

export interface UsersResponse {
  users: DbUser[];
  roles: Role[];
  canManage: boolean;
  databaseError?: string;
  credentialKind?: CredentialKind;
  // False when users may have no roles, e.g. a PostgreSQL database's owner.
  rolesRequired?: boolean;
  // True when database-scoped roles are granted on the instance's managed databases.
  databasesSupported?: boolean;
}

export interface Credentials {
  id: string;
  username: string;
  password: string;
  // Empty until the database is reachable.
  uri?: string;
  secret: string;
  // Further connection details some engines need, e.g. the model name.
  properties?: Record<string, string>;
}

export interface NewUser {
  username: string;
  password?: string;
  roles: RoleGrant[];
}

export interface InstanceTarget {
  namespace: string;
  instanceName: string;
}
