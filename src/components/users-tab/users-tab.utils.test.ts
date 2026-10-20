import { describe, expect, it } from 'vitest';
import { DbUser } from 'types/users.types';
import { availableActions } from './users-tab.utils';

const managed: DbUser = {
  id: 'mongo-app-1a2b3c',
  username: 'app',
  roles: [],
  managed: true,
  system: false,
  phase: 'Ready',
};

describe('availableActions', () => {
  it('offers every action on a managed user', () => {
    expect(availableActions(managed, true)).toEqual([
      'editRoles',
      'showCredentials',
      'resetPassword',
      'delete',
    ]);
  });

  it('offers nothing without permission to manage users', () => {
    expect(availableActions(managed, false)).toEqual([]);
  });

  it('offers nothing on users the plugin does not manage', () => {
    expect(availableActions({ ...managed, id: undefined, managed: false }, true)).toEqual([]);
    expect(availableActions({ ...managed, id: undefined, managed: false, system: true }, true)).toEqual([]);
  });

  it('offers nothing on a user being deleted', () => {
    expect(availableActions({ ...managed, phase: 'Deleting' }, true)).toEqual([]);
  });

  it('hides password actions when the password lives in the user\'s own Secret', () => {
    expect(availableActions({ ...managed, ownPasswordSecret: true }, true)).toEqual([
      'editRoles',
      'delete',
    ]);
  });

  it('keeps failed users editable so they can be fixed', () => {
    expect(availableActions({ ...managed, phase: 'Failed' }, true)).toContain('editRoles');
  });
});
