export const Messages = {
  editRolesTitle: (username: string) => `Edit roles of ${username}`,
  passwordHelper: 'Leave empty to generate a strong password.',
  roles: 'Roles',
  role: 'Role',
  database: 'Database',
  allDatabases: 'All databases',
  addRole: 'Add role',
  removeRole: 'Remove role',
  cancel: 'Cancel',
  create: 'Create',
  save: 'Save',
  usernameRequired: 'Enter a username.',
  passwordLength: (min: number, max: number) =>
    `Use ${min} to ${max} characters, or leave empty to generate one.`,
  rolesRequired: 'Grant at least one role.',
  roleRequired: 'Select a role.',
  databaseRequired: 'Enter a database.',
};
