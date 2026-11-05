import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { queryKeys } from '@/lib/query/keys';
import { logout } from '@/features/auth/api';
import type { Me, Role } from '@/lib/api/types';
import { addMember, createWorkspace, listMembers } from './api';

export function useMembers(workspaceId: string) {
	return useQuery({
		queryKey: queryKeys.members(workspaceId),
		queryFn: () => listMembers(workspaceId),
		staleTime: 60_000,
	});
}

export function useCreateWorkspace() {
	const queryClient = useQueryClient();
	const navigate = useNavigate();

	return useMutation({
		mutationFn: createWorkspace,
		onSuccess: async (workspace) => {
			queryClient.setQueryData<Me>(queryKeys.me, (current) => {
				if (!current) return current;
				if (current.workspaces.some((item) => item.id === workspace.id)) {
					return current;
				}
				return {
					...current,
					workspaces: [...current.workspaces, workspace],
				};
			});

			await queryClient.invalidateQueries({ queryKey: queryKeys.me });
			await navigate({
				to: '/w/$workspaceSlug/monitors',
				params: { workspaceSlug: workspace.slug },
			});
		},
	});
}

export function useAddMember(workspaceId: string) {
	const queryClient = useQueryClient();

	return useMutation({
		mutationFn: (input: { email: string; role: Role }) =>
			addMember(workspaceId, input),
		onSuccess: () =>
			queryClient.invalidateQueries({
				queryKey: queryKeys.members(workspaceId),
			}),
	});
}

export function useLogout() {
	const queryClient = useQueryClient();

	return useMutation({
		mutationFn: logout,
		onSettled: () => {
			queryClient.clear();
			window.location.assign('/login');
		},
	});
}
