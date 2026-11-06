import { DatabaseConnect } from 'types/databases.types';

export const Messages = {
  tabLabel: 'Databases',
  title: 'Databases',
  subtitle:
    'Databases of this instance and who owns them. Grant users read or readWrite on a database in the Users tab.',
  createDatabase: 'Create database',
  loadFailed: (reason: string) => `Failed to load databases: ${reason}`,
  unsupported: 'Databases of this instance are not managed by OpenEverest.',
  readOnly:
    "You can view databases. Creating and changing them takes permission to read this instance's connection details.",
  databaseError: (reason: string) =>
    `Databases created outside OpenEverest can't be listed: ${reason}`,
  noDatabases: 'No databases are managed yet. Create one, owned by a user of this instance.',
  columns: {
    name: 'Database',
    owner: 'Owner',
    connect: 'Who can connect',
    status: 'Status',
    actions: 'Actions',
  },
  connect: {
    Public: 'Every user',
    Granted: 'Owner and granted users',
  } satisfies Record<DatabaseConnect, string>,
  connectHelp: {
    Public:
      "PostgreSQL's default: every user can connect, but only the owner and users granted a role on it can use its tables.",
    Granted: 'Only the owner and users granted read or readWrite on it can connect.',
  } satisfies Record<DatabaseConnect, string>,
  systemDatabase: 'System',
  unmanagedDatabase: 'Not managed',
  systemDatabaseHint: 'Created by PostgreSQL, the operator or the provider.',
  unmanagedDatabaseHint: 'Created outside OpenEverest.',
  actions: { edit: 'Edit', delete: 'Delete' },
  databaseActions: (name: string) => `Actions for ${name}`,
  // Form
  createTitle: 'Create database',
  editTitle: (name: string) => `Edit database ${name}`,
  name: 'Name',
  nameHelper: 'Lowercase letters, digits and underscores.',
  nameRequired: 'Enter a name.',
  nameInvalid: 'Use 1-63 lowercase letters, digits and underscores, starting with a letter or an underscore.',
  owner: 'Owner',
  ownerHelper: 'Owns the database and the tables it creates. Run migrations as this user.',
  ownerRequired: 'Choose the owner.',
  noOwners: 'Create the owner in the Users tab first; it needs no roles.',
  whoCanConnect: 'Who can connect',
  cancel: 'Cancel',
  create: 'Create',
  save: 'Save',
  // Delete
  deleteTitle: (name: string) => `Delete database ${name}?`,
  deleteBody:
    'OpenEverest stops managing the database. Users granted read or readWrite on it lose that access.',
  dropData: 'Also drop the database and all its data',
  dropWarning: 'This permanently deletes the database and everything in it.',
  typeToConfirm: (name: string) => `Type ${name} to confirm`,
  keepData: 'The database and its data are kept.',
  delete: 'Delete',
  deleteAndDrop: 'Delete and drop data',
};
