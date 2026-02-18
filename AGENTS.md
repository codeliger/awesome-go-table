comments are only allowed on functions, methods, and constant declarations.
comments always end with a period.
code that uses the same variables or makes related function calls should be cuddled.
switches should be used instead of any else if chains.
strings used more than once should be declared as a constant.
errors should always be constants of a struct type Error that satisfies the error interface.
errors should always be defined in pkg/types/errors.
struct tags are always snake case.
never put more than once space between two lines of code.
when writing methods start with the method stub with the default case return statement, then write a unit test for it, then run the unit test to make sure the test casefails, then write the code for the method, run the unit test again to make sure it passes.
