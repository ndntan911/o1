import {
	useInfiniteQuery,
	useMutation,
	useQuery,
	useQueryClient,
	type InfiniteData,
} from '@tanstack/react-query';
import { useNavigate, useParams } from '@tanstack/react-router';
import type {
	CreateMonitorInput,
	Monitor,
	MonitorListResponse,
	ResultsWindow,
	UpdateMonitorInput,
} from '@/lib/api/types';
import { queryKeys } from '@/lib/query/keys';
import { nextPageParam } from '@/lib/query/pagination';
import {
	createMonitor,
	deleteMonitor,
	getMonitor,
	getResults,
	getSummary,
	listMonitors,
	pauseMonitor,
	resumeMonitor,
	updateMonitor,
} from './api';

type MonitorPages = InfiniteData<MonitorListResponse, string | undefined>;

export function updateMonitorInList(
	pages: MonitorListResponse[],
	monitorId: string,
	patch: Partial<Monitor>,
): MonitorListResponse[] {
	return pages.map((page) => ({
		...page,
		monitors: page.monitors.map((monitor) =>
			monitor.id === monitorId ? { ...monitor, ...patch } : monitor,
		),
	}));
}

function pausedPatch(paused: boolean): Partial<Monitor> {
	return {
		paused,
		status: paused ? 'paused' : 'unknown',
	};
}

function useSetMonitorPaused(
	workspaceId: string,
	monitorId: string,
	paused: boolean,
) {
	const queryClient = useQueryClient();
	const monitorKey = queryKeys.monitor(workspaceId, monitorId);
	const listKey = queryKeys.monitors(workspaceId);

	return useMutation({
		mutationFn: () =>
			paused
				? pauseMonitor(workspaceId, monitorId)
				: resumeMonitor(workspaceId, monitorId),
		onMutate: async () => {
			await queryClient.cancelQueries({ queryKey: listKey });
			await queryClient.cancelQueries({ queryKey: monitorKey });

			const previousMonitor = queryClient.getQueryData<Monitor>(monitorKey);
			const previousLists = queryClient.getQueryData<MonitorPages>(listKey);

			queryClient.setQueryData<Monitor>(monitorKey, (current) =>
				current ? { ...current, ...pausedPatch(paused) } : current,
			);
			queryClient.setQueryData<MonitorPages>(listKey, (current) =>
				current
					? {
							...current,
							pages: updateMonitorInList(
								current.pages,
								monitorId,
								pausedPatch(paused),
							),
						}
					: current,
			);

			return { previousMonitor, previousLists };
		},
		onError: (_error, _variables, context) => {
			if (context?.previousMonitor !== undefined) {
				queryClient.setQueryData<Monitor>(monitorKey, context.previousMonitor);
			}
			if (context?.previousLists !== undefined) {
				queryClient.setQueryData<MonitorPages>(listKey, context.previousLists);
			}
		},
		onSettled: () =>
			Promise.all([
				queryClient.invalidateQueries({ queryKey: monitorKey }),
				queryClient.invalidateQueries({ queryKey: listKey }),
			]),
	});
}

export function useMonitors(workspaceId: string) {
	return useInfiniteQuery({
		queryKey: queryKeys.monitors(workspaceId),
		queryFn: ({ pageParam, signal }) =>
			listMonitors(workspaceId, pageParam, signal),
		initialPageParam: undefined as string | undefined,
		getNextPageParam: nextPageParam,
		staleTime: 10_000,
		refetchInterval: 15_000,
	});
}

export function useMonitor(workspaceId: string, monitorId: string) {
	return useQuery({
		queryKey: queryKeys.monitor(workspaceId, monitorId),
		queryFn: ({ signal }) => getMonitor(workspaceId, monitorId, signal),
		staleTime: 10_000,
		refetchInterval: 15_000,
	});
}

export function useCreateMonitor(workspaceId: string) {
	const queryClient = useQueryClient();

	return useMutation({
		mutationFn: (input: CreateMonitorInput) =>
			createMonitor(workspaceId, input),
		onSuccess: () =>
			Promise.all([
				queryClient.invalidateQueries({
					queryKey: queryKeys.monitors(workspaceId),
				}),
				queryClient.invalidateQueries({ queryKey: queryKeys.me }),
			]),
	});
}

export function useUpdateMonitor(workspaceId: string, monitorId: string) {
	const queryClient = useQueryClient();

	return useMutation({
		mutationFn: (input: UpdateMonitorInput) =>
			updateMonitor(workspaceId, monitorId, input),
		onSuccess: (monitor) => {
			queryClient.setQueryData(
				queryKeys.monitor(workspaceId, monitorId),
				monitor,
			);
			return Promise.all([
				queryClient.invalidateQueries({
					queryKey: queryKeys.monitor(workspaceId, monitorId),
				}),
				queryClient.invalidateQueries({
					queryKey: queryKeys.monitors(workspaceId),
				}),
				queryClient.invalidateQueries({
					queryKey: ['summary', workspaceId, monitorId],
				}),
				queryClient.invalidateQueries({
					queryKey: ['results', workspaceId, monitorId],
				}),
			]);
		},
	});
}

export function usePauseMonitor(workspaceId: string, monitorId: string) {
	return useSetMonitorPaused(workspaceId, monitorId, true);
}

export function useResumeMonitor(workspaceId: string, monitorId: string) {
	return useSetMonitorPaused(workspaceId, monitorId, false);
}

export function useDeleteMonitor(
	workspaceId: string,
	monitorId: string,
	workspaceSlug?: string,
) {
	const queryClient = useQueryClient();
	const navigate = useNavigate();
	const params = useParams({ strict: false }) as { workspaceSlug?: string };

	return useMutation({
		mutationFn: () => deleteMonitor(workspaceId, monitorId),
		onSuccess: async () => {
			queryClient.removeQueries({
				queryKey: queryKeys.monitor(workspaceId, monitorId),
			});
			await queryClient.invalidateQueries({
				queryKey: queryKeys.monitors(workspaceId),
			});
			await navigate({
				to: '/w/$workspaceSlug/monitors',
				params: {
					workspaceSlug: workspaceSlug ?? params.workspaceSlug ?? '',
				},
			});
		},
	});
}

export function useResults(
	workspaceId: string,
	monitorId: string,
	resultsWindow: ResultsWindow,
) {
	return useInfiniteQuery({
		queryKey: queryKeys.results(workspaceId, monitorId, resultsWindow),
		queryFn: ({ pageParam, signal }) =>
			getResults(workspaceId, monitorId, resultsWindow, pageParam, signal),
		initialPageParam: undefined as string | undefined,
		getNextPageParam: nextPageParam,
		staleTime: 10_000,
		refetchInterval: 30_000,
	});
}

export function useSummary(
	workspaceId: string,
	monitorId: string,
	resultsWindow: ResultsWindow,
) {
	return useQuery({
		queryKey: queryKeys.summary(workspaceId, monitorId, resultsWindow),
		queryFn: ({ signal }) =>
			getSummary(workspaceId, monitorId, resultsWindow, signal),
		staleTime: 10_000,
		refetchInterval: 30_000,
	});
}
