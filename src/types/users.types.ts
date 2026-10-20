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
  // DatabaseUser name; absent for users the plugin doesn't manage.
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

export interface UsersResponse {
  users: DbUser[];
  roles: Role[];
  canManage: boolean;
  databaseError?: string;
}

export interface Credentials {
  id: string;
  username: string;
  password: string;
  // Empty until the database is reachable.
  uri?: string;
  secret: string;
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
