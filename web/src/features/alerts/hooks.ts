import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { queryKeys } from '@/lib/query/keys';
import {
	createAlertChannel,
	deleteAlertChannel,
	getMonitorAlertChannel,
	listAlertChannels,
	putEmailAlertChannel,
	setMonitorAlertChannel,
	testAlertChannel,
	updateAlertChannel,
	type CreateAlertChannelInput,
	type PutEmailAlertChannelInput,
	type UpdateAlertChannelInput,
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

function useInvalidateChannelLists(workspaceId: string) {
	const queryClient = useQueryClient();

	return () =>
		Promise.all([
			queryClient.invalidateQueries({
				queryKey: queryKeys.alertChannels(workspaceId),
			}),
			queryClient.invalidateQueries({
				queryKey: ['monitor-alert-channels', workspaceId],
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
		]);
}

export function useCreateAlertChannel(workspaceId: string) {
	const invalidate = useInvalidateChannelLists(workspaceId);

	return useMutation({
		mutationFn: (input: CreateAlertChannelInput) =>
			createAlertChannel(workspaceId, input),
		onSuccess: invalidate,
	});
}

export function useUpdateAlertChannel(workspaceId: string) {
	const invalidate = useInvalidateChannelLists(workspaceId);

	return useMutation({
		mutationFn: ({
			channelId,
			input,
		}: {
			channelId: string;
			input: UpdateAlertChannelInput;
		}) => updateAlertChannel(workspaceId, channelId, input),
		onSuccess: invalidate,
	});
}

export function useDeleteAlertChannel(workspaceId: string) {
	const invalidate = useInvalidateChannelLists(workspaceId);

	return useMutation({
		mutationFn: (channelId: string) =>
			deleteAlertChannel(workspaceId, channelId),
		onSuccess: invalidate,
	});
}

export function useTestAlertChannel(workspaceId: string) {
	return useMutation({
		mutationFn: (channelId: string) => testAlertChannel(workspaceId, channelId),
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
