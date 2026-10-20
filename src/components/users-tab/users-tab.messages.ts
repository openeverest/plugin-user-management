export const Messages = {
  tabLabel: 'Users',
  title: 'Database users',
  subtitle: 'Users that applications and people connect to this database with.',
  createUser: 'Create user',
  loadFailed: (reason: string) => `Failed to load users: ${reason}`,
  readOnly:
    "You can view users. Creating and changing them takes permission to read this instance's connection details.",
  noUsers: 'No users yet. Create one for your application instead of sharing the admin account.',
  columns: {
    username: 'Username',
    authDatabase: 'Auth database',
    roles: 'Roles',
    actions: 'Actions',
  },
  systemUser: 'System',
  systemUserHint: 'Created and used by the operator; it cannot be changed here.',
  unmanagedUser: 'Not editable',
  unmanagedUserHint: "Has roles this plugin doesn't grant, so it can only be changed directly in the database.",
  actions: {
    editRoles: 'Edit roles',
    resetPassword: 'Reset password',
    delete: 'Delete',
  },
  userActions: (username: string) => `Actions for ${username}`,
  createdTitle: (username: string) => `User ${username} created`,
  passwordResetTitle: (username: string) => `New password for ${username}`,
  resetPasswordTitle: 'Reset password?',
  resetPasswordBody: (username: string) =>
    `A new password will be generated for ${username}. Applications using the current password will fail to authenticate on their next connection.`,
  resetPasswordConfirm: 'Reset password',
  deleteTitle: 'Delete user?',
  deleteBody: (username: string) =>
    `${username} will be removed from the database. Applications using it will lose access.`,
  deleteConfirm: 'Delete',
};
