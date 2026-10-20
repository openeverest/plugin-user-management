import { useState } from 'react';
import { ConfirmDialog } from 'components/confirm-dialog/confirm-dialog';
import { CredentialsDialog } from 'components/credentials-dialog/credentials-dialog';
import { UserFormDialog } from 'components/user-form-dialog/user-form-dialog';
import {
  useCreateUser,
  useDeleteUser,
  useResetUserPassword,
  useUpdateUserRoles,
} from 'hooks/useUsers';
import { Credentials, InstanceTarget, Role } from 'types/users.types';
import { UsersDialogState } from '../users-tab.types';
import { Messages } from '../users-tab.messages';
import { StoredCredentialsDialog } from './stored-credentials-dialog/stored-credentials-dialog';

interface UsersDialogsProps {
  target: InstanceTarget;
  roles: Role[];
  dialog: UsersDialogState;
  onClose: () => void;
}

interface ShownCredentials {
  title: string;
  credentials: Credentials;
}

export const UsersDialogs = ({ target, roles, dialog, onClose }: UsersDialogsProps) => {
  const [shown, setShown] = useState<ShownCredentials | null>(null);
  const createUser = useCreateUser(target);
  const updateRoles = useUpdateUserRoles(target);
  const resetPassword = useResetUserPassword(target);
  const deleteUser = useDeleteUser(target);

  const close = () => {
    [createUser, updateRoles, resetPassword, deleteUser].forEach((m) => m.reset());
    onClose();
  };

  if (shown) {
    return (
      <CredentialsDialog
        title={shown.title}
        credentials={shown.credentials}
        onClose={() => setShown(null)}
      />
    );
  }

  switch (dialog?.kind) {
    case 'create':
      return (
        <UserFormDialog
          mode="create"
          roles={roles}
          isSubmitting={createUser.isPending}
          error={createUser.error?.message}
          onClose={close}
          onSubmit={(user) =>
            createUser.mutate(user, {
              onSuccess: (credentials) => {
                setShown({ title: Messages.createdTitle(credentials.username), credentials });
                close();
              },
            })
          }
        />
      );
    case 'editRoles':
      return (
        <UserFormDialog
          mode="editRoles"
          roles={roles}
          username={dialog.user.username}
          initialRoles={dialog.user.roles}
          isSubmitting={updateRoles.isPending}
          error={updateRoles.error?.message}
          onClose={close}
          onSubmit={({ roles: grants }) =>
            updateRoles.mutate({ id: dialog.user.id, roles: grants }, { onSuccess: close })
          }
        />
      );
    case 'showCredentials':
      return <StoredCredentialsDialog target={target} user={dialog.user} onClose={close} />;
    case 'resetPassword':
      return (
        <ConfirmDialog
          title={Messages.resetPasswordTitle}
          confirmLabel={Messages.resetPasswordConfirm}
          isSubmitting={resetPassword.isPending}
          error={resetPassword.error?.message}
          onClose={close}
          onConfirm={() =>
            resetPassword.mutate(dialog.user.id, {
              onSuccess: (credentials) => {
                setShown({ title: Messages.passwordResetTitle(credentials.username), credentials });
                close();
              },
            })
          }
        >
          {Messages.resetPasswordBody(dialog.user.username)}
        </ConfirmDialog>
      );
    case 'delete':
      return (
        <ConfirmDialog
          title={Messages.deleteTitle}
          confirmLabel={Messages.deleteConfirm}
          destructive
          isSubmitting={deleteUser.isPending}
          error={deleteUser.error?.message}
          onClose={close}
          onConfirm={() => deleteUser.mutate(dialog.user.id, { onSuccess: close })}
        >
          {Messages.deleteBody(dialog.user.username)}
        </ConfirmDialog>
      );
    default:
      return null;
  }
};
