import { UserPhase } from './users.types';

// Who can connect: every user, or only the owner and users granted a role on it.
export type DatabaseConnect = 'Public' | 'Granted';

export interface Database {
  // InstanceDatabase name; absent for databases the plugin doesn't manage.
  id?: string;
  name: string;
  // The owner's username, and its user's id for managed databases.
  owner?: string;
  ownerId?: string;
  connect?: DatabaseConnect;
  deletionPolicy?: 'Retain' | 'Delete';
  managed: boolean;
  system: boolean;
  phase?: UserPhase;
  message?: string;
}

export interface DatabasesResponse {
  databases: Database[];
  // False when the instance's engine doesn't manage databases.
  supported: boolean;
  canManage: boolean;
  databaseError?: string;
}

export interface DatabaseSettings {
  ownerId: string;
  connect: DatabaseConnect;
}

export interface NewDatabase extends DatabaseSettings {
  name: string;
}
