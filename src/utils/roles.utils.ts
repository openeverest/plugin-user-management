import { Role, RoleGrant } from 'types/users.types';
import { Messages } from './roles.messages';

export const findRole = (roles: Role[], name: string) => roles.find((r) => r.name === name);

// Engines whose roles all apply to the whole instance (PostgreSQL, model API
// keys) have nothing to pick a database for.
export const hasDatabaseRoles = (roles: Role[]) => roles.some((r) => r.scope === 'database');

export const formatRoleGrant = ({ role, database }: RoleGrant) =>
  database ? Messages.roleOnDatabase(role, database) : role;
