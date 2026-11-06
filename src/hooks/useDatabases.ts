import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { createDatabase, deleteDatabase, getDatabases, updateDatabase } from 'api/databases-api';
import { usePluginApi } from 'components/plugin-api-context/plugin-api.context';
import { DatabaseSettings, DatabasesResponse, NewDatabase } from 'types/databases.types';
import { InstanceTarget } from 'types/users.types';

// Databases settle within seconds once the operator applies them.
const SETTLING_REFETCH_MS = 3_000;

export const databasesQueryKey = (target: InstanceTarget) => [
  'db-databases',
  target.namespace,
  target.instanceName,
];

const isSettling = (data?: DatabasesResponse) =>
  !!data?.databases.some((d) => d.phase === 'Pending' || d.phase === 'Deleting');

export const useDatabases = (target: InstanceTarget, enabled = true) => {
  const api = usePluginApi();
  return useQuery({
    queryKey: databasesQueryKey(target),
    queryFn: () => getDatabases(api.fetch, target),
    enabled,
    refetchInterval: (query) => (isSettling(query.state.data) ? SETTLING_REFETCH_MS : false),
  });
};

// Every change refreshes the databases, and the users whose roles depend on them.
const useDatabasesMutation = <TVariables>(
  target: InstanceTarget,
  mutate: (variables: TVariables) => Promise<void>
) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: mutate,
    onSuccess: () =>
      Promise.all([
        queryClient.invalidateQueries({ queryKey: databasesQueryKey(target) }),
        queryClient.invalidateQueries({ queryKey: ['db-users', target.namespace, target.instanceName] }),
      ]),
  });
};

export const useCreateDatabase = (target: InstanceTarget) => {
  const api = usePluginApi();
  return useDatabasesMutation(target, (db: NewDatabase) => createDatabase(api.fetch, target, db));
};

export const useUpdateDatabase = (target: InstanceTarget) => {
  const api = usePluginApi();
  return useDatabasesMutation(target, ({ id, settings }: { id: string; settings: DatabaseSettings }) =>
    updateDatabase(api.fetch, target, id, settings)
  );
};

export const useDeleteDatabase = (target: InstanceTarget) => {
  const api = usePluginApi();
  return useDatabasesMutation(target, ({ id, drop }: { id: string; drop: boolean }) =>
    deleteDatabase(api.fetch, target, id, drop)
  );
};
