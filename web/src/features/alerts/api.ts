import { apiRequest } from '@/lib/api/client';
import type { AlertChannel, MonitorAlertState } from '@/lib/api/types';

export interface PutEmailAlertChannelInput {
	recipients: string[];
	enabled: boolean;
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
