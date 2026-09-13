export type UserMode = 'beginner' | 'advanced'

const USER_MODE_KEY = 'nofx_user_mode'

// Every authenticated user lands on the traders page. This build no longer
// ships a separate beginner onboarding flow (it was tied to the removed
// Claw402 wallet), so post-auth routing is a fixed destination.
export const POST_AUTH_PATH = '/traders'

export function getUserMode(): UserMode | null {
  const value = localStorage.getItem(USER_MODE_KEY)
  if (value === 'beginner' || value === 'advanced') {
    return value
  }
  return null
}

export function setUserMode(mode: UserMode) {
  localStorage.setItem(USER_MODE_KEY, mode)
}

export function getPostAuthPath(_mode?: UserMode | null): string {
  return POST_AUTH_PATH
}
