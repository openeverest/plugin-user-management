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

export interface DbUser {
  id: string;
  username: string;
  database?: string;
  roles: RoleGrant[];
  system: boolean;
  manageable: boolean;
}

export interface UsersResponse {
  users: DbUser[];
  roles: Role[];
  canManage: boolean;
}

export interface Credentials {
  username: string;
  password: string;
  uri: string;
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
