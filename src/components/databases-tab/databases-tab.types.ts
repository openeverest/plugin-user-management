import { Database } from 'types/databases.types';

export type DatabaseAction = 'edit' | 'delete';

// A database declared by an InstanceDatabase, which the actions operate on.
export type ManagedDatabase = Database & { id: string };

export type DatabasesDialogState =
  | { kind: 'create' }
  | { kind: DatabaseAction; database: ManagedDatabase }
  | null;

export interface OwnerOption {
  id: string;
  username: string;
}
