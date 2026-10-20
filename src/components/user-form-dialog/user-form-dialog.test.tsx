import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { Role } from 'types/users.types';
import { UserFormDialog } from './user-form-dialog';
import { Messages } from './user-form-dialog.messages';

const roles: Role[] = [
  { name: 'readWrite', scope: 'database', description: 'Read and write' },
  { name: 'readAnyDatabase', scope: 'cluster', description: 'Read all' },
];

const selectRole = (index: number, name: string) => {
  fireEvent.mouseDown(screen.getAllByRole('combobox')[index]);
  fireEvent.click(within(screen.getByRole('listbox')).getByText(name));
};

describe('UserFormDialog', () => {
  afterEach(cleanup);

  it('submits a new user only once the form is valid', () => {
    const onSubmit = vi.fn();
    render(
      <UserFormDialog mode="create" roles={roles} isSubmitting={false} onSubmit={onSubmit} onClose={vi.fn()} />
    );

    fireEvent.click(screen.getByRole('button', { name: Messages.create }));
    expect(onSubmit).not.toHaveBeenCalled();
    expect(screen.getByText(Messages.usernameRequired)).toBeTruthy();

    fireEvent.change(screen.getByLabelText(Messages.username), { target: { value: ' app ' } });
    selectRole(0, 'readWrite');
    fireEvent.change(screen.getByLabelText(Messages.database), { target: { value: 'shop' } });
    fireEvent.click(screen.getByRole('button', { name: Messages.addRole }));
    selectRole(1, 'readAnyDatabase');
    fireEvent.click(screen.getByRole('button', { name: Messages.create }));

    expect(onSubmit).toHaveBeenCalledWith({
      username: 'app',
      password: undefined,
      roles: [{ role: 'readWrite', database: 'shop' }, { role: 'readAnyDatabase' }],
    });
  });

  it('edits the roles of an existing user', () => {
    const onSubmit = vi.fn();
    render(
      <UserFormDialog
        mode="editRoles"
        roles={roles}
        username="app"
        initialRoles={[{ role: 'readWrite', database: 'shop' }, { role: 'readAnyDatabase' }]}
        isSubmitting={false}
        onSubmit={onSubmit}
        onClose={vi.fn()}
      />
    );
    expect(screen.queryByLabelText(Messages.username)).toBeNull();

    fireEvent.click(screen.getAllByRole('button', { name: Messages.removeRole })[0]);
    fireEvent.click(screen.getByRole('button', { name: Messages.save }));

    expect(onSubmit).toHaveBeenCalledWith({
      username: 'app',
      password: undefined,
      roles: [{ role: 'readAnyDatabase' }],
    });
  });
});
