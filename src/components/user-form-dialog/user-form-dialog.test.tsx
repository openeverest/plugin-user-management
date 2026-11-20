import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { Role } from 'types/users.types';
import { UserFormDialog } from './user-form-dialog';
import { Messages } from './user-form-dialog.messages';
import {
  API_KEY_TERMS,
  PASSWORD_TERMS,
  S3_TERMS,
  TermsContext,
} from 'components/terms-context/terms.context';

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

    fireEvent.change(screen.getByLabelText(PASSWORD_TERMS.username), { target: { value: ' app ' } });
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

  it('creates a user that reads credentials from an existing Secret', () => {
    const onSubmit = vi.fn();
    render(
      <UserFormDialog
        mode="create"
        roles={roles}
        externalCredentialsSupported
        isSubmitting={false}
        onSubmit={onSubmit}
        onClose={vi.fn()}
      />
    );

    fireEvent.change(screen.getByLabelText(PASSWORD_TERMS.username), { target: { value: 'app' } });
    selectRole(0, 'readAnyDatabase');
    fireEvent.click(screen.getByLabelText(Messages.useExistingSecret));
    fireEvent.click(screen.getByRole('button', { name: Messages.create }));
    expect(onSubmit).not.toHaveBeenCalled();
    expect(screen.getByText(Messages.secretNameRequired)).toBeTruthy();

    fireEvent.change(screen.getByLabelText(Messages.secretName), {
      target: { value: 'app-vault-credentials' },
    });
    fireEvent.change(screen.getByLabelText(Messages.secretKey), {
      target: { value: 'credential' },
    });
    fireEvent.click(screen.getByRole('button', { name: Messages.create }));
    expect(onSubmit).toHaveBeenCalledWith({
      username: 'app',
      passwordSecretRef: { name: 'app-vault-credentials', key: 'credential' },
      roles: [{ role: 'readAnyDatabase' }],
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
    expect(screen.queryByLabelText(PASSWORD_TERMS.username)).toBeNull();

    fireEvent.click(screen.getAllByRole('button', { name: Messages.removeRole })[0]);
    fireEvent.click(screen.getByRole('button', { name: Messages.save }));

    expect(onSubmit).toHaveBeenCalledWith({
      username: 'app',
      password: undefined,
      roles: [{ role: 'readAnyDatabase' }],
    });
  });

  it('generates API keys instead of asking for a password', () => {
    const onSubmit = vi.fn();
    render(
      <TermsContext.Provider value={API_KEY_TERMS}>
        <UserFormDialog mode="create" roles={roles} isSubmitting={false} onSubmit={onSubmit} onClose={vi.fn()} />
      </TermsContext.Provider>
    );
    expect(screen.getByText(API_KEY_TERMS.createTitle)).toBeTruthy();
    expect(screen.queryByLabelText(PASSWORD_TERMS.secret)).toBeNull();
    expect(screen.queryByLabelText(API_KEY_TERMS.secret)).toBeNull();

    fireEvent.change(screen.getByLabelText(API_KEY_TERMS.username), { target: { value: 'chat-app' } });
    selectRole(0, 'readAnyDatabase');
    fireEvent.click(screen.getByRole('button', { name: Messages.create }));

    expect(onSubmit).toHaveBeenCalledWith({
      username: 'chat-app',
      password: undefined,
      roles: [{ role: 'readAnyDatabase' }],
    });
  });

  it('has no database field or extra rows for a single instance-wide role', () => {
    const onSubmit = vi.fn();
    const invoke: Role[] = [{ name: 'invoke', scope: 'cluster', description: 'Call the model' }];
    render(
      <TermsContext.Provider value={API_KEY_TERMS}>
        <UserFormDialog mode="create" roles={invoke} isSubmitting={false} onSubmit={onSubmit} onClose={vi.fn()} />
      </TermsContext.Provider>
    );
    expect(screen.queryByLabelText(Messages.database)).toBeNull();
    expect(screen.queryByRole('button', { name: Messages.addRole })).toBeNull();

    fireEvent.change(screen.getByLabelText(API_KEY_TERMS.username), { target: { value: 'chat-app' } });
    fireEvent.click(screen.getByRole('button', { name: Messages.create }));

    expect(onSubmit).toHaveBeenCalledWith({
      username: 'chat-app',
      password: undefined,
      roles: [{ role: 'invoke' }],
    });
  });

  it('keeps the database field when roles are granted per database', () => {
    render(
      <UserFormDialog mode="create" roles={roles} isSubmitting={false} onSubmit={vi.fn()} onClose={vi.fn()} />
    );
    expect(screen.getByLabelText(Messages.database)).toBeTruthy();
  });

  it('grants an S3 role on a bucket', () => {
    const onSubmit = vi.fn();
    const bucketRoles: Role[] = [
      { name: 'readWrite', scope: 'bucket', description: 'Read and write objects' },
    ];
    render(
      <TermsContext.Provider value={S3_TERMS}>
        <UserFormDialog
          mode="create"
          roles={bucketRoles}
          isSubmitting={false}
          onSubmit={onSubmit}
          onClose={vi.fn()}
        />
      </TermsContext.Provider>
    );
    expect(screen.queryByLabelText(Messages.database)).toBeNull();
    fireEvent.change(screen.getByLabelText(S3_TERMS.username), { target: { value: 'uploader' } });
    fireEvent.change(screen.getByLabelText(Messages.bucket), { target: { value: 'uploads' } });
    fireEvent.click(screen.getByRole('button', { name: Messages.create }));

    expect(onSubmit).toHaveBeenCalledWith({
      username: 'uploader',
      password: undefined,
      roles: [{ role: 'readWrite', bucket: 'uploads' }],
    });
  });
});

describe('UserFormDialog with managed databases', () => {
  afterEach(cleanup);

  it('picks the database of a database-scoped role from the managed ones', () => {
    const onSubmit = vi.fn();
    render(
      <UserFormDialog
        mode="create"
        roles={roles}
        databases={['orders', 'billing']}
        rolesRequired={false}
        isSubmitting={false}
        onSubmit={onSubmit}
        onClose={vi.fn()}
      />
    );
    fireEvent.change(screen.getByLabelText(PASSWORD_TERMS.username), { target: { value: 'app' } });
    fireEvent.click(screen.getByRole('button', { name: Messages.addRole }));
    selectRole(0, 'readWrite');
    fireEvent.mouseDown(screen.getAllByRole('combobox')[1]);
    const options = within(screen.getByRole('listbox')).getAllByRole('option').map((o) => o.textContent);
    expect(options).toEqual(['orders', 'billing']);
    fireEvent.click(within(screen.getByRole('listbox')).getByText('billing'));
    fireEvent.click(screen.getByRole('button', { name: Messages.create }));
    expect(onSubmit).toHaveBeenCalledWith({
      username: 'app',
      password: undefined,
      roles: [{ role: 'readWrite', database: 'billing' }],
    });
  });

  it('says to create a database first when there is none', () => {
    render(
      <UserFormDialog mode="create" roles={roles} databases={[]} isSubmitting={false} onSubmit={vi.fn()} onClose={vi.fn()} />
    );
    selectRole(0, 'readWrite');
    expect(screen.getByText(Messages.noDatabases)).toBeTruthy();
  });

  it('starts a new user without roles when the engine allows it', () => {
    const onSubmit = vi.fn();
    render(
      <UserFormDialog
        mode="create"
        roles={roles}
        databases={[]}
        rolesRequired={false}
        isSubmitting={false}
        onSubmit={onSubmit}
        onClose={vi.fn()}
      />
    );
    fireEvent.change(screen.getByLabelText(PASSWORD_TERMS.username), { target: { value: 'orders_owner' } });
    expect(screen.queryByRole('button', { name: Messages.removeRole })).toBeNull();
    expect(screen.getByText(Messages.noRoles)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: Messages.create }));
    expect(onSubmit).toHaveBeenCalledWith({ username: 'orders_owner', password: undefined, roles: [] });
  });

  it('still requires a role when the engine does', () => {
    render(
      <UserFormDialog mode="create" roles={roles} isSubmitting={false} onSubmit={vi.fn()} onClose={vi.fn()} />
    );
    expect((screen.getByRole('button', { name: Messages.removeRole }) as HTMLButtonElement).disabled).toBe(true);
  });
});
