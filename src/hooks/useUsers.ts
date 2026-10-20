import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  createUser,
  deleteUser,
  getUserCredentials,
  getUsers,
  resetUserPassword,
  updateUserRoles,
} from 'api/users-api';
import { usePluginApi } from 'components/plugin-api-context/plugin-api.context';
import { InstanceTarget, NewUser, RoleGrant, UsersResponse } from 'types/users.types';

// Users settle within seconds once the operator applies them.
const SETTLING_REFETCH_MS = 3_000;

const usersQueryKey = (target: InstanceTarget) => [
  'db-users',
  target.namespace,
  target.instanceName,
];

const isSettling = (data?: UsersResponse) =>
  !!data?.users.some((u) => u.phase === 'Pending' || u.phase === 'Deleting');

export const useUsers = (target: InstanceTarget) => {
  const api = usePluginApi();
  return useQuery({
    queryKey: usersQueryKey(target),
    queryFn: () => getUsers(api.fetch, target),
    refetchInterval: (query) => (isSettling(query.state.data) ? SETTLING_REFETCH_MS : false),
  });
};

export const useUserCredentials = (target: InstanceTarget, id: string) => {
  const api = usePluginApi();
  return useQuery({
    queryKey: ['db-user-credentials', target.namespace, target.instanceName, id],
    queryFn: () => getUserCredentials(api.fetch, target, id),
    // Passwords are only kept while the dialog shows them.
    gcTime: 0,
    staleTime: 0,
    retry: false,
  });
};

// Every change refreshes the user list.
const useUsersMutation = <TVariables, TResult>(
  target: InstanceTarget,
  mutate: (variables: TVariables) => Promise<TResult>
) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: mutate,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: usersQueryKey(target) }),
  });
};

export const useCreateUser = (target: InstanceTarget) => {
  const api = usePluginApi();
  return useUsersMutation(target, (user: NewUser) => createUser(api.fetch, target, user));
};

export const useUpdateUserRoles = (target: InstanceTarget) => {
  const api = usePluginApi();
  return useUsersMutation(target, ({ id, roles }: { id: string; roles: RoleGrant[] }) =>
    updateUserRoles(api.fetch, target, id, roles)
  );
};

export const useResetUserPassword = (target: InstanceTarget) => {
  const api = usePluginApi();
  return useUsersMutation(target, (id: string) => resetUserPassword(api.fetch, target, id));
};

export const useDeleteUser = (target: InstanceTarget) => {
  const api = usePluginApi();
  return useUsersMutation(target, (id: string) => deleteUser(api.fetch, target, id));
};
