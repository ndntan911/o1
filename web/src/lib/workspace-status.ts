import { useSyncExternalStore } from 'react';

let suspended = false;
const listeners = new Set<() => void>();

function emit(): void {
	for (const listener of listeners) listener();
}

export function subscribe(listener: () => void): () => void {
	listeners.add(listener);
	return () => {
		listeners.delete(listener);
	};
}

export function getWorkspaceSuspended(): boolean {
	return suspended;
}

export function markWorkspaceSuspended(): void {
	if (suspended) return;
	suspended = true;
	emit();
}

export function clearWorkspaceSuspended(): void {
	if (!suspended) return;
	suspended = false;
	emit();
}

function getServerSnapshot(): boolean {
	return false;
}

export function useWorkspaceSuspended(): boolean {
	return useSyncExternalStore(
		subscribe,
		getWorkspaceSuspended,
		getServerSnapshot,
	);
}

export function resetWorkspaceStatusForTests(): void {
	suspended = false;
}
