import { Role, RoleGrant } from 'types/users.types';
import { findRole } from 'utils/roles.utils';
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

export const initialFormValues = (grants: RoleGrant[] = []): UserFormValues => ({
  username: '',
  password: '',
  roles: grants.length
    ? grants.map(({ role, database }) => ({ role, database: database ?? '' }))
    : [EMPTY_ROLE_GRANT],
});

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
