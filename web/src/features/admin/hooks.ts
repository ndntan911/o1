import {
	useInfiniteQuery,
	useMutation,
	useQueryClient,
} from '@tanstack/react-query';
import type {
	AdminMonitorFilters,
	AdminWorkspaceFilters,
	SecurityEventFilters,
} from '@/lib/api/types';
import { queryKeys } from '@/lib/query/keys';
import { nextPageParam } from '@/lib/query/pagination';
import {
	listAdminMonitors,
	listAdminWorkspaces,
	listSecurityEvents,
	suspendWorkspace,
	unsuspendWorkspace,
} from './api';

const ADMIN_QUERY_STALE_TIME = 60_000;

function invalidateAdminMutations(
	queryClient: ReturnType<typeof useQueryClient>,
): Promise<void[]> {
	return Promise.all([
		queryClient.invalidateQueries({ queryKey: ['admin', 'workspaces'] }),
		queryClient.invalidateQueries({ queryKey: ['admin', 'security-events'] }),
	]);
}

export function useAdminWorkspaces(filters: AdminWorkspaceFilters) {
	return useInfiniteQuery({
		queryKey: queryKeys.adminWorkspaces(filters),
		queryFn: ({ pageParam, signal }) =>
			listAdminWorkspaces(filters, pageParam, signal),
		initialPageParam: undefined as string | undefined,
		getNextPageParam: nextPageParam,
		staleTime: ADMIN_QUERY_STALE_TIME,
	});
}

export function useAdminMonitors(filters: AdminMonitorFilters) {
	return useInfiniteQuery({
		queryKey: queryKeys.adminMonitors(filters),
		queryFn: ({ pageParam, signal }) =>
			listAdminMonitors(filters, pageParam, signal),
		initialPageParam: undefined as string | undefined,
		getNextPageParam: nextPageParam,
		staleTime: ADMIN_QUERY_STALE_TIME,
	});
}

export function useSecurityEvents(filters: SecurityEventFilters) {
	return useInfiniteQuery({
		queryKey: queryKeys.adminSecurityEvents(filters),
		queryFn: ({ pageParam, signal }) =>
			listSecurityEvents(filters, pageParam, signal),
		initialPageParam: undefined as string | undefined,
		getNextPageParam: nextPageParam,
		staleTime: ADMIN_QUERY_STALE_TIME,
	});
}

export function useSuspendWorkspace() {
	const queryClient = useQueryClient();

	return useMutation({
		mutationFn: ({
			workspaceId,
			reason,
		}: {
			workspaceId: string;
			reason: string;
		}) => suspendWorkspace(workspaceId, reason),
		onSuccess: () => invalidateAdminMutations(queryClient),
	});
}

export function useUnsuspendWorkspace() {
	const queryClient = useQueryClient();

	return useMutation({
		mutationFn: (workspaceId: string) => unsuspendWorkspace(workspaceId),
		onSuccess: () => invalidateAdminMutations(queryClient),
	});
}
