/**
 * An intent is a meaning that an element is meant to convey with its appearance.
 */
export enum Intent {
  /**
   * Indicates a dangerous action or event. This is an issue blocking an entire workflow.
   */
  Danger = "Danger",
  /**
   * Indicates a neutral state. This intent is neither positive nor negative, and can be used for
   * many elements in the same group.
   */
  Default = "Default",
  /**
   * Indicates a primary action to take. There should typically only be one primary element in a
   * group of elements.
   */
  Primary = "Primary",
  /**
   * Indicates a successful action. This intent should only be used to indicate a result, never an
   * action itself.
   */
  Success = "Success",
  /**
   * Indicates a warning. This is something that the user needs to know about and potentially fix,
   * but not something that will prevent the user from continuing.
   */
  Warning = "Warning",
}
