/**
 * Creates a type where each prop in `T` is not allowed to be assigned. Most useful when you want
 * to either require multiple related React props or disallow all of them.
 *
 * @example
 * interface ControlledPopupProps {
 *   isOpen: boolean;
 *   setIsOpen: (isOpen: boolean) => void;
 * }
 *
 * type UncontrolledPopupProps = NeverProps<ControlledPopupProps>;
 *
 * type PopupControlProps = ControlledPopupProps | UncontrolledPopupProps;
 *
 * type PopupProps = PopupControlProps & ...;
 *
 */
export type NeverProps<T extends object> = Partial<Record<keyof T, never>>;
