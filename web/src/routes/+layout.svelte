<script lang="ts">
	import '../app.css';
	import { browser } from '$app/environment';
	import PageShell from '$lib/shared/components/PageShell.svelte';
	import ApprovalOverlay from '$lib/security/ApprovalOverlay.svelte';

	let { children } = $props();

	// Apply dark mode from localStorage
	if (browser) {
		const t = localStorage.getItem('theme');
		if (t === 'dark' || (!t && matchMedia('(prefers-color-scheme: dark)').matches)) {
			document.documentElement.classList.add('dark');
		}
	}
</script>

<svelte:head>
	<title>Cordon</title>
</svelte:head>

<PageShell>
	{@render children()}
</PageShell>

<ApprovalOverlay />
