import { UserPhase } from 'types/users.types';
import { UserAction } from './users-tab.types';

export const Messages = {
  tabLabel: 'Users',
  title: 'Database users',
  subtitle: 'Users that applications and people connect to this database with.',
  createUser: 'Create user',
  loadFailed: (reason: string) => `Failed to load users: ${reason}`,
  databaseError: (reason: string) =>
    `Users that exist only in the database can't be shown right now: ${reason}`,
  readOnly:
    "You can view users. Creating and changing them takes permission to read this instance's connection details.",
  noUsers: 'No users yet. Create one for your application instead of sharing the admin account.',
  columns: {
    username: 'Username',
    authDatabase: 'Auth database',
    roles: 'Roles',
    status: 'Status',
    actions: 'Actions',
  },
  phases: {
    Pending: 'Applying',
    Ready: 'Ready',
    Failed: 'Failed',
    Deleting: 'Deleting',
  } satisfies Record<UserPhase, string>,
  systemUser: 'System',
  systemUserHint: 'Created and used by the operator; it cannot be changed here.',
  unmanagedUser: 'Not managed',
  unmanagedUserHint: 'Created outside OpenEverest; change it directly in the database.',
  actions: {
    editRoles: 'Edit roles',
    showCredentials: 'Show credentials',
    resetPassword: 'Reset password',
    delete: 'Delete',
  } satisfies Record<UserAction, string>,
  userActions: (username: string) => `Actions for ${username}`,
  createdTitle: (username: string) => `User ${username} declared`,
  credentialsTitle: (username: string) => `Credentials of ${username}`,
  passwordResetTitle: (username: string) => `New password for ${username}`,
  credentialsFailed: (reason: string) => `Failed to load credentials: ${reason}`,
  resetPasswordTitle: 'Reset password?',
  resetPasswordBody: (username: string) =>
    `A new password will be generated for ${username}. Applications using the current password will fail to authenticate on their next connection.`,
  resetPasswordConfirm: 'Reset password',
  deleteTitle: 'Delete user?',
  deleteBody: (username: string) =>
    `${username} will be removed from the database. Applications using it will lose access.`,
  deleteConfirm: 'Delete',
};
