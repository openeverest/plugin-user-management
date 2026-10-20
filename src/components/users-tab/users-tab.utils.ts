import { DbUser } from 'types/users.types';
import { ManagedUser, UserAction } from './users-tab.types';

export const isManaged = (user: DbUser): user is ManagedUser => user.managed && !!user.id;

// Actions available on a user, in menu order.
export const availableActions = (user: DbUser, canManage: boolean): UserAction[] => {
  if (!canManage || !isManaged(user) || user.phase === 'Deleting') {
    return [];
  }
  const actions: UserAction[] = ['editRoles'];
  if (!user.ownPasswordSecret) {
    actions.push('showCredentials', 'resetPassword');
  }
  actions.push('delete');
  return actions;
};
