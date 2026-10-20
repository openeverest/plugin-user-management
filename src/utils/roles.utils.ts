import { Role, RoleGrant } from 'types/users.types';
import { Messages } from './roles.messages';

export const findRole = (roles: Role[], name: string) => roles.find((r) => r.name === name);

export const formatRoleGrant = ({ role, database }: RoleGrant) =>
  database ? Messages.roleOnDatabase(role, database) : role;
