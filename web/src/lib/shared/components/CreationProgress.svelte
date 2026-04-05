<script lang="ts">
	import { connectCreationSSE } from '$lib/shared/api/client';
	import type { ProgressEvent } from '$lib/shared/api/types';

	let { workspaceId, onready }: { workspaceId: string; onready?: () => void } = $props();

	let events: ProgressEvent[] = $state([]);
	let currentMessage = $state('Starting...');
	let estimatedSecs = $state(0);
	let startTime = $state(0);
	let done = $state(false);
	let error = $state('');
	let elapsed = $state(0);
	let timer: ReturnType<typeof setInterval> | undefined;

	const progressPct = $derived.by(() => {
		if (done && !error) return 100;
		if (estimatedSecs <= 0) return 0;
		const pct = (elapsed / estimatedSecs) * 100;
		return Math.min(pct, 95); // cap at 95% until done
	});

	$effect(() => {
		startTime = Date.now();

		timer = setInterval(() => {
			elapsed = (Date.now() - startTime) / 1000;
		}, 250);

		const cleanup = connectCreationSSE(workspaceId, (evt) => {
			events = [...events, evt];
			currentMessage = evt.message;

			if (evt.estimated_secs && estimatedSecs === 0) {
				estimatedSecs = evt.estimated_secs;
			}

			if (evt.done) {
				done = true;
				if (evt.error) {
					error = evt.error;
				} else {
					clearInterval(timer);
					onready?.();
				}
			}
		});

		return () => {
			cleanup();
			clearInterval(timer);
		};
	});

	const steps = [
		'resolving_branch',
		'building_image',
		'creating_container',
		'configuring_git',
		'cloning_repo',
		'post_create',
		'done'
	];
	const stepLabels: Record<string, string> = {
		resolving_branch: 'Resolve branch',
		building_image: 'Build image',
		creating_container: 'Create container',
		configuring_git: 'Configure git',
		cloning_repo: 'Clone repository',
		post_create: 'Post-create commands',
		done: 'Ready'
	};

	const completedSteps = $derived(new Set(events.map((e) => e.step)));
	const currentStep = $derived(events.length > 0 ? events[events.length - 1].step : '');

	function formatTime(secs: number): string {
		const m = Math.floor(secs / 60);
		const s = Math.floor(secs % 60);
		return m > 0 ? `${m}m ${s}s` : `${s}s`;
	}
</script>

<div class="flex h-full flex-col items-center justify-center p-8">
	<div class="w-full max-w-md space-y-6">
		<h2 class="text-center text-lg font-semibold text-foreground">
			{done && !error ? 'Workspace Ready' : 'Creating Workspace'}
		</h2>

		<!-- Progress bar -->
		<div class="space-y-2">
			<div class="h-2 w-full overflow-hidden rounded-full bg-surface-raised">
				<div
					class="h-full rounded-full transition-all duration-500 ease-linear {error
						? 'bg-danger-solid'
						: done
							? 'bg-success-text'
							: 'bg-primary'}"
					style="width: {progressPct}%"
				></div>
			</div>
			<div class="flex justify-between text-xs text-foreground-faint">
				<span>{currentMessage}</span>
				<span class="flex gap-2">
					{#if !done}
						<span>{formatTime(elapsed)} elapsed</span>
					{/if}
					{#if estimatedSecs > 0 && !done && estimatedSecs - elapsed > 1}
						<span>&middot; ~{formatTime(Math.max(0, estimatedSecs - elapsed))} remaining</span>
					{:else if done && !error}
						<span>{formatTime(elapsed)} total</span>
					{/if}
				</span>
			</div>
		</div>

		<!-- Step list -->
		<div class="space-y-1.5">
			{#each steps as step}
				{@const isCompleted = completedSteps.has(step) && step !== currentStep}
				{@const isCurrent = step === currentStep && !done}
				{@const isDone = step === 'done' && done && !error}
				<div
					class="flex items-center gap-2.5 rounded-lg px-3 py-1.5 text-sm {isCompleted || isDone
						? 'text-foreground-secondary'
						: isCurrent
							? 'bg-surface-raised text-foreground'
							: 'text-foreground-faint'}"
				>
					{#if isCompleted || isDone}
						<span class="text-success-text">&#10003;</span>
					{:else if isCurrent}
						<span class="inline-block h-3 w-3 animate-spin rounded-full border-2 border-primary border-t-transparent"></span>
					{:else}
						<span class="inline-block h-3 w-3 rounded-full border border-border"></span>
					{/if}
					<span>{stepLabels[step] || step}</span>
				</div>
			{/each}
		</div>

		{#if error}
			<div class="rounded-lg border border-danger-solid/20 bg-danger-bg p-4 text-sm text-danger-text">
				<p class="font-medium">Creation failed</p>
				<p class="mt-1 text-xs">{error}</p>
			</div>
			<a
				href="/"
				class="block rounded-lg border border-border-input px-4 py-2 text-center text-sm text-foreground-secondary transition-colors hover:bg-hover-subtle"
			>
				Back to Dashboard
			</a>
		{/if}
	</div>
</div>
