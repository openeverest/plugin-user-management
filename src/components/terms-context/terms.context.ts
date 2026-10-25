import { createContext, useContext } from 'react';
import { CredentialKind } from 'types/users.types';

// Wording that depends on how the instance's users authenticate: database
// users with passwords, or consumers of a model with API keys.
export interface Terms {
  title: string;
  subtitle: string;
  createUser: string;
  createTitle: string;
  username: string;
  noUsers: string;
  databaseError: (reason: string) => string;
  systemUserHint: string;
  unmanagedUserHint: string;
  secret: string;
  // Whether the create form offers to type the secret instead of generating it.
  customSecret: boolean;
  uri: string;
  pendingUri: string;
  resetPassword: string;
  resetPasswordTitle: string;
  resetPasswordBody: (username: string) => string;
  passwordResetTitle: (username: string) => string;
  createdTitle: (username: string) => string;
  deleteBody: (username: string) => string;
}

export const PASSWORD_TERMS: Terms = {
  title: 'Database users',
  subtitle: 'Users that applications and people connect to this database with.',
  createUser: 'Create user',
  createTitle: 'Create database user',
  username: 'Username',
  noUsers: 'No users yet. Create one for your application instead of sharing the admin account.',
  databaseError: (reason) =>
    `Users that exist only in the database can't be shown right now: ${reason}`,
  systemUserHint: 'Created and used by the operator; it cannot be changed here.',
  unmanagedUserHint: 'Created outside OpenEverest; change it directly in the database.',
  secret: 'Password',
  customSecret: true,
  uri: 'Connection string (in-cluster)',
  pendingUri:
    'The connection string appears once the database is reachable; it is also kept in the Secret.',
  resetPassword: 'Reset password',
  resetPasswordTitle: 'Reset password?',
  resetPasswordBody: (username) =>
    `A new password will be generated for ${username}. Applications using the current password will fail to authenticate on their next connection.`,
  passwordResetTitle: (username) => `New password for ${username}`,
  createdTitle: (username) => `User ${username} declared`,
  deleteBody: (username) =>
    `${username} will be removed from the database. Applications using it will lose access.`,
};

export const API_KEY_TERMS: Terms = {
  title: 'API keys',
  subtitle: 'Applications and people that call this model, each with its own API key.',
  createUser: 'Create API key',
  createTitle: 'Create API key',
  username: 'Name',
  noUsers:
    "No API keys yet. Create one per application instead of sharing the model's key from its connection details.",
  databaseError: (reason) => `API keys can't be shown or applied right now: ${reason}`,
  systemUserHint: "The model's own key, shown in its connection details; it cannot be changed here.",
  unmanagedUserHint: 'An API key Secret created outside OpenEverest.',
  secret: 'API key',
  customSecret: false,
  uri: 'Base URL',
  pendingUri:
    'The base URL appears once the model is reachable through the AI Gateway; it is also kept in the Secret.',
  resetPassword: 'Rotate API key',
  resetPasswordTitle: 'Rotate API key?',
  resetPasswordBody: (username) =>
    `A new API key will be generated for ${username}. Requests with the current key will be rejected within seconds.`,
  passwordResetTitle: (username) => `New API key for ${username}`,
  createdTitle: (username) => `API key for ${username} created`,
  deleteBody: (username) =>
    `The API key of ${username} will be revoked. Requests with it will be rejected within seconds.`,
};

export const termsFor = (kind?: CredentialKind): Terms =>
  kind === 'apiKey' ? API_KEY_TERMS : PASSWORD_TERMS;

export const TermsContext = createContext<Terms>(PASSWORD_TERMS);

export const useTerms = (): Terms => useContext(TermsContext);
