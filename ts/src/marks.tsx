/**
 * The vendors' own marks, for the buttons that sign somebody in with them.
 *
 * Inline and not fetched: a sign-in page that reaches a third party to draw
 * itself tells that third party who is signing in and when, before anybody has
 * agreed to anything. They are trademarks used as the vendors ask them to be --
 * on the button that signs somebody in with them -- and which one a button
 * carries is the deployment's to say (`auth.sso.providers[].mark`), never a
 * guess from its label.
 *
 * @module
 */

import type { ReactNode } from 'react'

/** The mark of `google`, `microsoft` or `github`, and nothing for anything else. */
export function Mark(props: { of?: string | undefined }): ReactNode {
	switch (props.of) {
		case 'google':
			return (
				<svg viewBox="0 0 48 48" aria-hidden="true">
					<path fill="#4285f4" d="M45 24c0-1.6-.1-2.7-.4-4H24v7.5h12c-.2 2-1.5 5-4.4 7l6.7 5.2C42.2 36 45 30.6 45 24Z" />
					<path fill="#34a853" d="M24 46c5.9 0 10.9-2 14.5-5.3l-6.9-5.4c-1.9 1.3-4.4 2.2-7.6 2.2-5.8 0-10.7-3.8-12.5-9.1l-7.1 5.5C8.1 41.1 15.4 46 24 46Z" />
					<path fill="#fbbc05" d="M11.5 28.4a13.4 13.4 0 0 1 0-8.7l-7.1-5.5a22 22 0 0 0 0 19.8l7.1-5.6Z" />
					<path fill="#ea4335" d="M24 9.5c3.3 0 5.5 1.4 6.8 2.6l5.9-5.8C33.1 3 29.1 1 24 1 15.4 1 8.1 5.9 4.4 13.2l7.1 5.5C13.3 13.3 18.2 9.5 24 9.5Z" />
				</svg>
			)
		case 'microsoft':
			return (
				<svg viewBox="0 0 21 21" aria-hidden="true">
					<path fill="#f25022" d="M0 0h10v10H0z" />
					<path fill="#7fba00" d="M11 0h10v10H11z" />
					<path fill="#00a4ef" d="M0 11h10v10H0z" />
					<path fill="#ffb900" d="M11 11h10v10H11z" />
				</svg>
			)
		case 'github':
			return (
				<svg viewBox="0 0 16 16" aria-hidden="true">
					<path
						fill="currentColor"
						d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82a7.4 7.4 0 0 1 2-.27c.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8Z"
					/>
				</svg>
			)
		default:
			return null
	}
}
