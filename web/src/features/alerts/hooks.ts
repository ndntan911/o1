import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { queryKeys } from '@/lib/query/keys';
import {
	getMonitorAlertChannel,
	listAlertChannels,
	putEmailAlertChannel,
	setMonitorAlertChannel,
	type PutEmailAlertChannelInput,
} from './api';

export function useAlertChannels(workspaceId: string) {
	return useQuery({
		queryKey: queryKeys.alertChannels(workspaceId),
		queryFn: () => listAlertChannels(workspaceId),
		staleTime: 30_000,
	});
}

export function useEmailAlertChannel(workspaceId: string) {
	return useQuery({
		queryKey: queryKeys.alertChannels(workspaceId),
		queryFn: () => listAlertChannels(workspaceId),
		select: (data) =>
			data.channels.find((channel) => channel.kind === 'email') ?? null,
		staleTime: 30_000,
	});
}

export function useSaveEmailAlertChannel(workspaceId: string) {
	const queryClient = useQueryClient();

	return useMutation({
		mutationFn: (input: PutEmailAlertChannelInput) =>
			putEmailAlertChannel(workspaceId, input),
		onSuccess: () =>
			Promise.all([
				queryClient.invalidateQueries({
					queryKey: queryKeys.alertChannels(workspaceId),
				}),
				queryClient.invalidateQueries({
					queryKey: ['monitor-alert-channel', workspaceId],
				}),
				queryClient.invalidateQueries({
					queryKey: ['monitor', workspaceId],
				}),
				queryClient.invalidateQueries({
					queryKey: queryKeys.monitors(workspaceId),
				}),
			]),
	});
}

export function useMonitorAlertChannel(
	workspaceId: string,
	monitorId: string,
	enabled = true,
) {
	return useQuery({
		queryKey: queryKeys.monitorAlertChannel(workspaceId, monitorId),
		queryFn: () => getMonitorAlertChannel(workspaceId, monitorId),
		enabled,
		staleTime: 15_000,
	});
}

export function useSetMonitorAlertChannel(
	workspaceId: string,
	monitorId: string,
) {
	const queryClient = useQueryClient();

	return useMutation({
		mutationFn: (enabled: boolean) =>
			setMonitorAlertChannel(workspaceId, monitorId, enabled),
		onSuccess: () =>
			Promise.all([
				queryClient.invalidateQueries({
					queryKey: queryKeys.monitorAlertChannel(workspaceId, monitorId),
				}),
				queryClient.invalidateQueries({
					queryKey: ['monitor', workspaceId],
				}),
				queryClient.invalidateQueries({
					queryKey: queryKeys.monitors(workspaceId),
				}),
			]),
	});
}
