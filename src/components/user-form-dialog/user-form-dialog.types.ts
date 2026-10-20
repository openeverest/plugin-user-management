export type UserFormMode = 'create' | 'editRoles';

export interface RoleGrantDraft {
  role: string;
  database: string;
}

export interface UserFormValues {
  username: string;
  // Empty means "generate one".
  password: string;
  roles: RoleGrantDraft[];
}

export interface RoleGrantErrors {
  role?: string;
  database?: string;
}

export interface UserFormErrors {
  username?: string;
  password?: string;
  roles?: string;
  roleRows: RoleGrantErrors[];
}

export const EMPTY_ROLE_GRANT: RoleGrantDraft = { role: '', database: '' };

export const PASSWORD_MIN_LENGTH = 8;
export const PASSWORD_MAX_LENGTH = 128;
