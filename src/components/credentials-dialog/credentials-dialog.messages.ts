export const Messages = {
  storedIn: (secret: string) =>
    `Stored in Secret ${secret} in this namespace, so applications can mount it. Anyone who can manage this instance's users can see it here.`,
  pendingUri:
    'The connection string appears once the database is reachable; it is also kept in the Secret.',
  username: 'Username',
  password: 'Password',
  uri: 'Connection string (in-cluster)',
  copy: 'Copy',
  copied: 'Copied',
  copyLabel: (field: string) => `Copy ${field}`,
  done: 'Done',
};
