<script lang="ts">
	import type { Snippet } from 'svelte';
	import { getHealth } from '$lib/shared/api/client';

	let { children }: { children: Snippet } = $props();

	let healthy = $state<boolean | null>(null);
	let currentPath = $state(typeof window !== 'undefined' ? window.location.pathname : '/');

	$effect(() => {
		getHealth()
			.then(() => (healthy = true))
			.catch(() => (healthy = false));
		const id = setInterval(() => {
			getHealth()
				.then(() => (healthy = true))
				.catch(() => (healthy = false));
		}, 30000);
		return () => clearInterval(id);
	});

	// Update path on navigation
	$effect(() => {
		function update() {
			currentPath = window.location.pathname;
		}
		window.addEventListener('popstate', update);
		return () => window.removeEventListener('popstate', update);
	});

	const navItems = [
		{ href: '/', label: 'Dashboard', exact: true },
		{ href: '/audit', label: 'Audit Log', exact: false },
		{ href: '/settings', label: 'Settings', exact: false }
	];

	function isActive(item: (typeof navItems)[0]) {
		if (item.exact) return currentPath === item.href;
		return currentPath.startsWith(item.href);
	}
</script>

<div class="flex min-h-screen bg-sidebar">
	<nav
		class="flex w-56 shrink-0 flex-col border-r border-sidebar-border bg-sidebar p-4 sticky top-0 h-screen overflow-y-auto"
	>
		<a href="/" class="mb-4 block px-3 py-2">
			<span class="flex items-center gap-2">
				<span
					class="flex h-6 w-6 shrink-0 items-center justify-center rounded-md bg-primary"
				>
					<span class="text-xs font-bold leading-none text-on-primary">C</span>
				</span>
				<h2 class="text-sm font-semibold tracking-tight text-foreground">Cordon</h2>
				{#if healthy === true}
					<span class="h-2 w-2 rounded-full bg-success-text" title="Healthy"></span>
				{:else if healthy === false}
					<span class="h-2 w-2 rounded-full bg-danger-solid" title="Unhealthy"></span>
				{/if}
			</span>
		</a>

		<ul class="flex-1 space-y-0.5">
			{#each navItems as item}
				<li>
					<a
						href={item.href}
						class="flex items-center gap-2.5 rounded-md px-3 py-2 text-sm transition-colors
							{isActive(item)
							? 'bg-sidebar-active text-foreground font-medium'
							: 'text-foreground-muted hover:bg-sidebar-hover hover:text-foreground'}"
					>
						{item.label}
					</a>
				</li>
			{/each}
		</ul>

		<div class="border-t border-sidebar-border pt-3 mt-2">
			<span class="px-3 text-xs text-foreground-faint">Zero-Trust Dev Environment</span>
		</div>
	</nav>

	<main class="min-w-0 flex-1 bg-page overflow-auto">
		<div class="p-6 md:p-8">
			{@render children()}
		</div>
	</main>
</div>
