import { Role, RoleGrant } from 'types/users.types';
import { findRole, hasDatabaseRoles } from 'utils/roles.utils';
import {
  EMPTY_ROLE_GRANT,
  PASSWORD_MAX_LENGTH,
  PASSWORD_MIN_LENGTH,
  RoleGrantDraft,
  UserFormErrors,
  UserFormMode,
  UserFormValues,
} from './user-form-dialog.types';
import { Messages } from './user-form-dialog.messages';

// A new user starts with one empty role row, or with the only role there is.
export const initialFormValues = (grants: RoleGrant[] = [], roles: Role[] = []): UserFormValues => {
  let rows: RoleGrantDraft[] = [EMPTY_ROLE_GRANT];
  if (grants.length) {
    rows = grants.map(({ role, database }) => ({ role, database: database ?? '' }));
  } else if (roles.length === 1) {
    rows = [{ ...EMPTY_ROLE_GRANT, role: roles[0].name }];
  }
  return { username: '', password: '', roles: rows };
};

// Database-scoped roles can be granted on many databases; otherwise each role
// can be granted once.
export const canAddRoleRow = (rows: RoleGrantDraft[], roles: Role[]) =>
  hasDatabaseRoles(roles) || rows.length < roles.length;

const isDatabaseScoped = (roles: Role[], name: string) =>
  findRole(roles, name)?.scope === 'database';

// Engine-specific rules (allowed names, reserved databases) are enforced by the
// backend; these checks only catch what every engine rejects.
export const validateUserForm = (
  values: UserFormValues,
  roles: Role[],
  mode: UserFormMode
): UserFormErrors => {
  const errors: UserFormErrors = { roleRows: [] };
  if (mode === 'create') {
    if (!values.username.trim()) {
      errors.username = Messages.usernameRequired;
    }
    const { length } = values.password;
    if (length > 0 && (length < PASSWORD_MIN_LENGTH || length > PASSWORD_MAX_LENGTH)) {
      errors.password = Messages.passwordLength(PASSWORD_MIN_LENGTH, PASSWORD_MAX_LENGTH);
    }
  }
  if (values.roles.length === 0) {
    errors.roles = Messages.rolesRequired;
  }
  errors.roleRows = values.roles.map(({ role, database }) => ({
    role: role ? undefined : Messages.roleRequired,
    database:
      isDatabaseScoped(roles, role) && !database.trim() ? Messages.databaseRequired : undefined,
  }));
  return errors;
};

export const hasErrors = ({ roleRows, ...fieldErrors }: UserFormErrors) =>
  Object.values(fieldErrors).some(Boolean) ||
  roleRows.some((row) => row.role || row.database);

export const toRoleGrants = (drafts: RoleGrantDraft[], roles: Role[]): RoleGrant[] =>
  drafts.map(({ role, database }) =>
    isDatabaseScoped(roles, role) ? { role, database: database.trim() } : { role }
  );
