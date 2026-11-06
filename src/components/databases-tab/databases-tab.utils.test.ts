import { describe, expect, it } from 'vitest';
import { Database } from 'types/databases.types';
import { DbUser } from 'types/users.types';
import {
  availableDatabaseActions,
  grantableDatabases,
  ownerOptions,
  validateDatabaseForm,
} from './databases-tab.utils';
import { Messages } from './databases-tab.messages';

const managed: Database = { id: 'pg-orders', name: 'orders', managed: true, system: false, phase: 'Ready' };

describe('databases tab utils', () => {
  it('validates database names and owners', () => {
    expect(validateDatabaseForm({ name: '', ownerId: '', connect: 'Public' }, true)).toEqual({
      name: Messages.nameRequired,
      ownerId: Messages.ownerRequired,
    });
    expect(validateDatabaseForm({ name: 'Orders-DB', ownerId: 'o', connect: 'Public' }, true)).toEqual({
      name: Messages.nameInvalid,
    });
    expect(validateDatabaseForm({ name: 'orders_2', ownerId: 'o', connect: 'Granted' }, true)).toEqual({});
    // The name can't change once created.
    expect(validateDatabaseForm({ name: '', ownerId: 'o', connect: 'Public' }, false)).toEqual({});
  });

  it('offers managed users as owners', () => {
    const users: DbUser[] = [
      { id: 'pg-owner', username: 'owner', roles: [], managed: true, system: false, phase: 'Ready' },
      { id: 'pg-gone', username: 'gone', roles: [], managed: true, system: false, phase: 'Deleting' },
      { username: 'postgres', roles: [], managed: false, system: true },
    ];
    expect(ownerOptions(users)).toEqual([{ id: 'pg-owner', username: 'owner' }]);
  });

  it('only acts on managed databases', () => {
    expect(availableDatabaseActions(managed, true)).toEqual(['edit', 'delete']);
    expect(availableDatabaseActions(managed, false)).toEqual([]);
    expect(availableDatabaseActions({ ...managed, phase: 'Deleting' }, true)).toEqual([]);
    expect(availableDatabaseActions({ name: 'app', managed: false, system: true }, true)).toEqual([]);
  });

  it('grants roles on managed databases only', () => {
    expect(
      grantableDatabases([
        managed,
        { ...managed, id: 'x', name: 'dropping', phase: 'Deleting' },
        { name: 'app', managed: false, system: true },
      ])
    ).toEqual(['orders']);
  });
});
