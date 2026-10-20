import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  createUser,
  deleteUser,
  getUsers,
  resetUserPassword,
  updateUserRoles,
} from 'api/users-api';
import { usePluginApi } from 'components/plugin-api-context/plugin-api.context';
import { InstanceTarget, NewUser, RoleGrant } from 'types/users.types';

const usersQueryKey = (target: InstanceTarget) => [
  'db-users',
  target.namespace,
  target.instanceName,
];

export const useUsers = (target: InstanceTarget) => {
  const api = usePluginApi();
  return useQuery({
    queryKey: usersQueryKey(target),
    queryFn: () => getUsers(api.fetch, target),
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
