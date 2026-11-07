import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { DatabaseFormDialog } from './database-form-dialog/database-form-dialog';
import { DeleteDatabaseDialog } from './delete-database-dialog/delete-database-dialog';
import { Messages } from './databases-tab.messages';

const owners = [
  { id: 'pg-orders-owner', username: 'orders_owner' },
  { id: 'pg-other', username: 'other' },
];

describe('DatabaseFormDialog', () => {
  afterEach(cleanup);

  it('creates a database once it has a valid name and an owner', () => {
    const onSubmit = vi.fn();
    render(<DatabaseFormDialog owners={owners} isSubmitting={false} onSubmit={onSubmit} onClose={vi.fn()} />);

    fireEvent.click(screen.getByRole('button', { name: Messages.create }));
    expect(onSubmit).not.toHaveBeenCalled();
    expect(screen.getByText(Messages.nameRequired)).toBeTruthy();
    expect(screen.getByText(Messages.ownerRequired)).toBeTruthy();

    fireEvent.change(screen.getByLabelText(Messages.name), { target: { value: ' orders ' } });
    fireEvent.mouseDown(screen.getByRole('combobox'));
    fireEvent.click(within(screen.getByRole('listbox')).getByText('orders_owner'));
    fireEvent.click(screen.getByLabelText(new RegExp(Messages.connect.Granted)));
    fireEvent.click(screen.getByRole('button', { name: Messages.create }));
    expect(onSubmit).toHaveBeenCalledWith({ name: 'orders', ownerId: 'pg-orders-owner', connect: 'Granted' });
  });

  it('edits the owner and who can connect, not the name', () => {
    const onSubmit = vi.fn();
    render(
      <DatabaseFormDialog
        initial={{ name: 'orders', ownerId: 'pg-orders-owner', connect: 'Granted' }}
        owners={owners}
        isSubmitting={false}
        onSubmit={onSubmit}
        onClose={vi.fn()}
      />
    );
    expect(screen.queryByLabelText(Messages.name)).toBeNull();
    fireEvent.mouseDown(screen.getByRole('combobox'));
    fireEvent.click(within(screen.getByRole('listbox')).getByText('other'));
    fireEvent.click(screen.getByLabelText(new RegExp(Messages.connect.Public)));
    fireEvent.click(screen.getByRole('button', { name: Messages.save }));
    expect(onSubmit).toHaveBeenCalledWith({ name: 'orders', ownerId: 'pg-other', connect: 'Public' });
  });

  it('points to the Users tab when there is no owner to choose', () => {
    render(<DatabaseFormDialog owners={[]} isSubmitting={false} onSubmit={vi.fn()} onClose={vi.fn()} />);
    expect(screen.getByText(Messages.noOwners)).toBeTruthy();
  });
});

describe('DeleteDatabaseDialog', () => {
  afterEach(cleanup);

  it('keeps the data by default', () => {
    const onConfirm = vi.fn();
    render(<DeleteDatabaseDialog name="orders" isSubmitting={false} onConfirm={onConfirm} onClose={vi.fn()} />);
    fireEvent.click(screen.getByRole('button', { name: Messages.delete }));
    expect(onConfirm).toHaveBeenCalledWith(false);
  });

  it('drops the data only once the name is typed', () => {
    const onConfirm = vi.fn();
    render(<DeleteDatabaseDialog name="orders" isSubmitting={false} onConfirm={onConfirm} onClose={vi.fn()} />);
    fireEvent.click(screen.getByLabelText(Messages.dropData));
    const confirm = screen.getByRole('button', { name: Messages.deleteAndDrop }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    fireEvent.change(screen.getByLabelText(Messages.typeToConfirm('orders')), { target: { value: 'order' } });
    expect(confirm.disabled).toBe(true);
    fireEvent.change(screen.getByLabelText(Messages.typeToConfirm('orders')), { target: { value: 'orders' } });
    fireEvent.click(confirm);
    expect(onConfirm).toHaveBeenCalledWith(true);
  });
});

describe('DatabaseFormDialog extensions', () => {
  afterEach(cleanup);

  const extensions = [
    { name: 'pg_trgm', trusted: true, description: 'text similarity' },
    { name: 'vector', trusted: false, description: 'vector data type' },
  ];

  it('creates a database with the extensions picked', () => {
    const onSubmit = vi.fn();
    render(
      <DatabaseFormDialog owners={owners} extensions={extensions} isSubmitting={false} onSubmit={onSubmit} onClose={vi.fn()} />
    );
    fireEvent.change(screen.getByLabelText(Messages.name), { target: { value: 'orders' } });
    fireEvent.mouseDown(screen.getByLabelText(Messages.owner));
    fireEvent.click(within(screen.getByRole('listbox', { name: Messages.owner })).getByText('orders_owner'));
    fireEvent.mouseDown(screen.getByLabelText(Messages.extensions));
    const options = screen.getByRole('listbox', { name: Messages.extensions });
    expect(within(options).getByText('vector data type')).toBeTruthy();
    fireEvent.click(within(options).getByText('vector'));
    fireEvent.click(screen.getByRole('button', { name: Messages.create }));
    expect(onSubmit).toHaveBeenCalledWith({
      name: 'orders',
      ownerId: 'pg-orders-owner',
      connect: 'Public',
      postgresql: { extensions: ['vector'] },
    });
  });

  it('removes an extension when editing', () => {
    const onSubmit = vi.fn();
    render(
      <DatabaseFormDialog
        initial={{ name: 'orders', ownerId: 'pg-orders-owner', connect: 'Public', postgresql: { extensions: ['vector', 'pg_trgm'] } }}
        owners={owners}
        extensions={extensions}
        isSubmitting={false}
        onSubmit={onSubmit}
        onClose={vi.fn()}
      />
    );
    fireEvent.click(within(screen.getByRole('button', { name: 'vector' })).getByTestId('CancelIcon'));
    fireEvent.click(screen.getByRole('button', { name: Messages.save }));
    expect(onSubmit).toHaveBeenCalledWith({
      name: 'orders',
      ownerId: 'pg-orders-owner',
      connect: 'Public',
      postgresql: { extensions: ['pg_trgm'] },
    });
  });

  it('has no extension picker on other engines', () => {
    const onSubmit = vi.fn();
    render(<DatabaseFormDialog owners={owners} isSubmitting={false} onSubmit={onSubmit} onClose={vi.fn()} />);
    expect(screen.queryByLabelText(Messages.extensions)).toBeNull();
  });
});
