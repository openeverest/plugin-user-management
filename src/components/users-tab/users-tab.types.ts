import { DbUser } from 'types/users.types';

export type UserAction = 'editRoles' | 'resetPassword' | 'delete';

export type UsersDialogState =
  | { kind: 'create' }
  | { kind: UserAction; user: DbUser }
  | null;
