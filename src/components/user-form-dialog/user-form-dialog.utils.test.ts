import { describe, expect, it } from 'vitest';
import { Role } from 'types/users.types';
import {
  canAddRoleRow,
  hasErrors,
  initialFormValues,
  toRoleGrants,
  validateUserForm,
} from './user-form-dialog.utils';

const roles: Role[] = [
  { name: 'readWrite', scope: 'database', description: '' },
  { name: 'readAnyDatabase', scope: 'cluster', description: '' },
];

describe('validateUserForm', () => {
  it('accepts a user with a generated password and valid roles', () => {
    const errors = validateUserForm(
      {
        username: 'app',
        password: '',
        roles: [
          { role: 'readWrite', database: 'shop' },
          { role: 'readAnyDatabase', database: '' },
        ],
      },
      roles,
      'create'
    );
    expect(hasErrors(errors)).toBe(false);
  });

  it('requires a database only for database-scoped roles', () => {
    const errors = validateUserForm(
      {
        username: 'app',
        password: '',
        roles: [
          { role: 'readWrite', database: ' ' },
          { role: 'readAnyDatabase', database: '' },
          { role: '', database: '' },
        ],
      },
      roles,
      'create'
    );
    expect(errors.roleRows[0].database).toBeDefined();
    expect(errors.roleRows[1]).toEqual({ role: undefined, database: undefined });
    expect(errors.roleRows[2].role).toBeDefined();
    expect(hasErrors(errors)).toBe(true);
  });

  it('rejects short passwords and missing usernames when creating', () => {
    const errors = validateUserForm(
      { username: ' ', password: 'short', roles: [{ role: 'readAnyDatabase', database: '' }] },
      roles,
      'create'
    );
    expect(errors.username).toBeDefined();
    expect(errors.password).toBeDefined();
  });

  it('ignores username and password when editing roles', () => {
    const errors = validateUserForm(
      { username: '', password: 'x', roles: [{ role: 'readAnyDatabase', database: '' }] },
      roles,
      'editRoles'
    );
    expect(hasErrors(errors)).toBe(false);
  });

  it('requires at least one role', () => {
    const errors = validateUserForm({ username: 'a', password: '', roles: [] }, roles, 'create');
    expect(errors.roles).toBeDefined();
  });
});

describe('toRoleGrants', () => {
  it('drops the database of cluster-scoped roles and trims the rest', () => {
    expect(
      toRoleGrants(
        [
          { role: 'readWrite', database: ' shop ' },
          { role: 'readAnyDatabase', database: 'leftover' },
        ],
        roles
      )
    ).toEqual([{ role: 'readWrite', database: 'shop' }, { role: 'readAnyDatabase' }]);
  });
});

describe('initialFormValues', () => {
  it('starts with one empty role row for new users', () => {
    expect(initialFormValues().roles).toEqual([{ role: '', database: '' }]);
  });

  it('pre-fills the roles of an edited user', () => {
    expect(initialFormValues([{ role: 'readAnyDatabase' }, { role: 'readWrite', database: 'x' }]).roles).toEqual([
      { role: 'readAnyDatabase', database: '' },
      { role: 'readWrite', database: 'x' },
    ]);
  });

  it('pre-selects the only role there is', () => {
    const invoke: Role[] = [{ name: 'invoke', scope: 'cluster', description: '' }];
    expect(initialFormValues([], invoke).roles).toEqual([{ role: 'invoke', database: '' }]);
    expect(initialFormValues([], roles).roles).toEqual([{ role: '', database: '' }]);
  });
});

describe('canAddRoleRow', () => {
  const clusterRoles: Role[] = [
    { name: 'pg_read_all_data', scope: 'cluster', description: '' },
    { name: 'pg_monitor', scope: 'cluster', description: '' },
  ];
  const row = (role: string) => ({ role, database: '' });

  it('always allows another row when roles are granted per database', () => {
    expect(canAddRoleRow([row('readWrite'), row('readAnyDatabase')], roles)).toBe(true);
  });

  it('allows one row per role when every role applies to the whole instance', () => {
    expect(canAddRoleRow([row('pg_monitor')], clusterRoles)).toBe(true);
    expect(canAddRoleRow([row('pg_monitor'), row('pg_read_all_data')], clusterRoles)).toBe(false);
  });
});
