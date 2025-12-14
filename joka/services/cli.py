
# make propt for yes/no confirmation.
def confirm(prompt: str | None = None) -> bool:
    """
    Prompt the user for a yes/no confirmation.
    Returns True if the user confirms with 'yes', False otherwise.
    """
    if prompt is None:
        prompt = "Are you sure you want to proceed? (only 'yes' will confirm): "
    
    response = input(prompt)
    return response.lower() == "yes"