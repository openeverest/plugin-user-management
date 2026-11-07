import { Database, DatabaseConnect } from 'types/databases.types';
import { DbUser } from 'types/users.types';
import { Messages } from './databases-tab.messages';
import { DatabaseAction, ManagedDatabase, OwnerOption } from './databases-tab.types';

// Mirrors the InstanceDatabase name pattern.
const NAME_PATTERN = /^[a-z_][a-z0-9_]{0,62}$/;

export const isManagedDatabase = (db: Database): db is ManagedDatabase => db.managed && !!db.id;

export const availableDatabaseActions = (db: Database, canManage: boolean): DatabaseAction[] =>
  canManage && isManagedDatabase(db) && db.phase !== 'Deleting' ? ['edit', 'delete'] : [];

// Owners are users the plugin manages and isn't deleting.
export const ownerOptions = (users: DbUser[]): OwnerOption[] =>
  users
    .filter((u) => u.managed && u.id && u.phase !== 'Deleting')
    .map((u) => ({ id: u.id as string, username: u.username }));

export interface DatabaseFormValues {
  name: string;
  ownerId: string;
  connect: DatabaseConnect;
}

export interface DatabaseFormErrors {
  name?: string;
  ownerId?: string;
}

export const validateDatabaseForm = (
  values: DatabaseFormValues,
  isCreate: boolean
): DatabaseFormErrors => {
  const errors: DatabaseFormErrors = {};
  if (isCreate) {
    if (!values.name.trim()) {
      errors.name = Messages.nameRequired;
    } else if (!NAME_PATTERN.test(values.name.trim())) {
      errors.name = Messages.nameInvalid;
    }
  }
  if (!values.ownerId) {
    errors.ownerId = Messages.ownerRequired;
  }
  return errors;
};

// Names of the databases users can be granted roles on.
export const grantableDatabases = (databases: Database[]) =>
  databases.filter((d) => d.managed && d.phase !== 'Deleting').map((d) => d.name);
