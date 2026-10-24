import { UserPhase } from 'types/users.types';
import { UserAction } from './users-tab.types';

export const Messages = {
  tabLabel: 'Users',
  loadFailed: (reason: string) => `Failed to load users: ${reason}`,
  readOnly:
    "You can view users. Creating and changing them takes permission to read this instance's connection details.",
  columns: {
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
  unmanagedUser: 'Not managed',
  actions: {
    editRoles: 'Edit roles',
    showCredentials: 'Show credentials',
    delete: 'Delete',
  } satisfies Record<Exclude<UserAction, 'resetPassword'>, string>,
  userActions: (username: string) => `Actions for ${username}`,
  credentialsTitle: (username: string) => `Credentials of ${username}`,
  credentialsFailed: (reason: string) => `Failed to load credentials: ${reason}`,
  deleteTitle: 'Delete user?',
  deleteConfirm: 'Delete',
};
