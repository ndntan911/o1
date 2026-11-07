import { apiRequest } from '@/lib/api/client';
import type {
	AlertChannel,
	AlertChannelKind,
	MonitorAlertState,
	MonitorChannelListResponse,
} from '@/lib/api/types';

export interface PutEmailAlertChannelInput {
	recipients: string[];
	enabled: boolean;
}

export interface CreateAlertChannelInput {
	kind: Exclude<AlertChannelKind, 'email'>;
	name: string;
	enabled: boolean;
	url?: string;
	signing_secret?: string;
	bot_token?: string;
	chat_id?: string;
}

export interface UpdateAlertChannelInput {
	name?: string;
	enabled?: boolean;
	url?: string;
	signing_secret?: string;
	clear_signing_secret?: boolean;
	bot_token?: string;
	chat_id?: string;
}

function alertChannelsPath(workspaceId: string): string {
	return `/workspaces/${workspaceId}/alert-channels`;
}

function monitorAlertChannelPath(
	workspaceId: string,
	monitorId: string,
): string {
	return `/workspaces/${workspaceId}/monitors/${monitorId}/alert-channel`;
}

export function listAlertChannels(
	workspaceId: string,
): Promise<{ channels: AlertChannel[] }> {
	return apiRequest<{ channels: AlertChannel[] }>(
		alertChannelsPath(workspaceId),
	);
}

export function putEmailAlertChannel(
	workspaceId: string,
	input: PutEmailAlertChannelInput,
): Promise<void> {
	return apiRequest<void>(`${alertChannelsPath(workspaceId)}/email`, {
		method: 'PUT',
		body: input,
	});
}

export function createAlertChannel(
	workspaceId: string,
	input: CreateAlertChannelInput,
): Promise<AlertChannel> {
	return apiRequest<AlertChannel>(alertChannelsPath(workspaceId), {
		method: 'POST',
		body: input,
	});
}

export function updateAlertChannel(
	workspaceId: string,
	channelId: string,
	input: UpdateAlertChannelInput,
): Promise<AlertChannel> {
	return apiRequest<AlertChannel>(
		`${alertChannelsPath(workspaceId)}/${channelId}`,
		{ method: 'PATCH', body: input },
	);
}

export function deleteAlertChannel(
	workspaceId: string,
	channelId: string,
): Promise<void> {
	return apiRequest<void>(`${alertChannelsPath(workspaceId)}/${channelId}`, {
		method: 'DELETE',
	});
}

export function testAlertChannel(
	workspaceId: string,
	channelId: string,
): Promise<void> {
	return apiRequest<void>(
		`${alertChannelsPath(workspaceId)}/${channelId}/test`,
		{ method: 'POST' },
	);
}

export function listMonitorAlertChannels(
	workspaceId: string,
	monitorId: string,
): Promise<MonitorChannelListResponse> {
	return apiRequest<MonitorChannelListResponse>(
		`/workspaces/${workspaceId}/monitors/${monitorId}/alert-channels`,
	);
}

export function setMonitorChannel(
	workspaceId: string,
	monitorId: string,
	channelId: string,
	enabled: boolean,
): Promise<void> {
	return apiRequest<void>(
		`/workspaces/${workspaceId}/monitors/${monitorId}/alert-channels/${channelId}`,
		{ method: 'PATCH', body: { enabled } },
	);
}

export function getMonitorAlertChannel(
	workspaceId: string,
	monitorId: string,
): Promise<MonitorAlertState> {
	return apiRequest<MonitorAlertState>(
		monitorAlertChannelPath(workspaceId, monitorId),
	);
}

export function setMonitorAlertChannel(
	workspaceId: string,
	monitorId: string,
	enabled: boolean,
): Promise<void> {
	return apiRequest<void>(monitorAlertChannelPath(workspaceId, monitorId), {
		method: 'PATCH',
		body: { enabled },
	});
}
