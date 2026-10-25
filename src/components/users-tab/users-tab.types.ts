import { DbUser } from 'types/users.types';

export type UserAction = 'editRoles' | 'showCredentials' | 'resetPassword' | 'delete';

// A user declared by an InstanceUser, which the actions operate on.
export type ManagedUser = DbUser & { id: string };

export type UsersDialogState =
  | { kind: 'create' }
  | { kind: UserAction; user: ManagedUser }
  | null;
