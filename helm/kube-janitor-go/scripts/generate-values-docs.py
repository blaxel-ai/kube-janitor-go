#!/usr/bin/env python3

"""
Generate values documentation from values.yaml for the Helm chart.
This script parses values.yaml and generates a markdown table of all configurable values.
"""

import os
import sys
import re
from typing import Dict, Any, List, Tuple

# Check Python version
if sys.version_info < (3, 6):
    print("Error: Python 3.6 or higher is required.")
    print(f"Current version: {sys.version}")
    sys.exit(1)

try:
    import yaml
except ImportError:
    print("Error: PyYAML is required but not installed.")
    print("Please install it with: pip install pyyaml")
    sys.exit(1)

def parse_yaml_with_comments(file_path: str) -> Tuple[Dict[str, Any], Dict[str, str]]:
    """Parse YAML file and extract comments for each key."""
    with open(file_path, 'r') as f:
        lines = f.readlines()
    
    # Parse the YAML normally
    with open(file_path, 'r') as f:
        values = yaml.safe_load(f)
    
    # Extract comments
    comments = {}
    for i, line in enumerate(lines):
        # Look for key: value patterns
        match = re.match(r'^(\s*)([a-zA-Z_][a-zA-Z0-9_]*):(.*)$', line)
        if match:
            indent = len(match.group(1))
            key = match.group(2)
            
            # Look for comments above this line
            comment_lines = []
            j = i - 1
            while j >= 0:
                comment_match = re.match(r'^\s*#\s*(.*)$', lines[j])
                if comment_match:
                    comment_lines.insert(0, comment_match.group(1))
                    j -= 1
                elif lines[j].strip() == '':
                    j -= 1
                else:
                    break
            
            if comment_lines:
                # Build the full path to this key
                path = get_key_path(lines, i, key, indent)
                comments[path] = ' '.join(comment_lines)
    
    return values, comments

def get_key_path(lines: List[str], line_index: int, key: str, indent: int) -> str:
    """Build the full path to a key based on indentation."""
    path_parts = [key]
    current_indent = indent
    
    # Work backwards to find parent keys
    for i in range(line_index - 1, -1, -1):
        match = re.match(r'^(\s*)([a-zA-Z_][a-zA-Z0-9_]*):(.*)$', lines[i])
        if match:
            parent_indent = len(match.group(1))
            if parent_indent < current_indent:
                path_parts.insert(0, match.group(2))
                current_indent = parent_indent
                if parent_indent == 0:
                    break
    
    return '.'.join(path_parts)

def flatten_dict(d: Dict[str, Any], parent_key: str = '', sep: str = '.') -> Dict[str, Any]:
    """Flatten a nested dictionary."""
    items = []
    for k, v in d.items():
        new_key = f"{parent_key}{sep}{k}" if parent_key else k
        if isinstance(v, dict) and v:  # Only recurse if dict is not empty
            items.extend(flatten_dict(v, new_key, sep=sep).items())
        else:
            items.append((new_key, v))
    return dict(items)

def format_value(value: Any) -> str:
    """Format a value for display in markdown."""
    if value is None or value == '':
        return '`""`'
    elif isinstance(value, bool):
        return f'`{str(value).lower()}`'
    elif isinstance(value, (int, float)):
        return f'`{value}`'
    elif isinstance(value, str):
        return f'`"{value}"`'
    elif isinstance(value, list):
        if len(value) == 0:
            return '`[]`'
        else:
            return 'See values.yaml'
    elif isinstance(value, dict):
        if len(value) == 0:
            return '`{}`'
        else:
            return 'See values.yaml'
    else:
        return 'See values.yaml'

def generate_values_table(values_file: str) -> str:
    """Generate a markdown table from values.yaml."""
    values, comments = parse_yaml_with_comments(values_file)
    flattened = flatten_dict(values)
    
    # Start building the table
    lines = [
        "| Parameter | Description | Default |",
        "|-----------|-------------|---------|"
    ]
    
    # Sort keys for consistent output
    for key in sorted(flattened.keys()):
        value = flattened[key]
        
        # Skip certain internal values
        if key.endswith('.') or 'resources.' in key and key.count('.') > 1:
            continue
            
        # Get description from comments or use default
        description = comments.get(key, '')
        if not description:
            # Try to find a partial match
            for comment_key, comment_value in comments.items():
                if key.endswith('.' + comment_key) or comment_key.endswith('.' + key.split('.')[-1]):
                    description = comment_value
                    break
        
        if not description:
            description = "See values.yaml"
        
        # Format the row
        formatted_value = format_value(value)
        lines.append(f"| `{key}` | {description} | {formatted_value} |")
    
    return '\n'.join(lines)

def update_readme(readme_file: str, table: str) -> None:
    """Update the README.md file with the generated table."""
    with open(readme_file, 'r') as f:
        content = f.read()
    
    # Find the Values section
    pattern = r'(## Values\s*\n\s*\n)((?:\|.*\|\s*\n)+)((?:\s*\n)?##)'
    match = re.search(pattern, content, re.MULTILINE)
    
    if match:
        # Replace the existing table
        new_content = content[:match.start(2)] + table + '\n' + content[match.start(3):]
        
        with open(readme_file, 'w') as f:
            f.write(new_content)
        
        print(f"Updated {readme_file} successfully!")
    else:
        print("Could not find Values section in README.md")
        print("Generated table:")
        print(table)

def main():
    # Get the script directory
    script_dir = os.path.dirname(os.path.abspath(__file__))
    chart_dir = os.path.dirname(script_dir)
    
    values_file = os.path.join(chart_dir, 'values.yaml')
    readme_file = os.path.join(chart_dir, 'README.md')
    
    if not os.path.exists(values_file):
        print(f"Error: values.yaml not found at {values_file}")
        sys.exit(1)
    
    print(f"Generating values documentation from {values_file}...")
    
    # Generate the table
    table = generate_values_table(values_file)
    
    print("Generated values table:")
    print(table)
    
    # Update README if it exists
    if os.path.exists(readme_file):
        print(f"\nUpdating {readme_file}...")
        update_readme(readme_file, table)
    else:
        print(f"\n{readme_file} not found. Table not written to file.")
    
    print("\nDone!")

if __name__ == '__main__':
    main() 